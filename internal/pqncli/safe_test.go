package pqncli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSafeWriterStripsTerminalControls(t *testing.T) {
	var out bytes.Buffer
	in := "harmless\x1b[2J\x1b[31mRED\x1b]0;PWNED\x07 title\rspoof\x7f\u0085\u009b next\tcol\nline2 é 日本"
	n, err := safeWriter{&out}.Write([]byte(in))
	if err != nil || n != len(in) {
		t.Fatalf("n=%d err=%v, want the input length and no error", n, err)
	}
	got := out.String()
	for _, bad := range []string{"\x1b", "\x07", "\r", "\x7f", "\u0085", "\u009b"} {
		if strings.Contains(got, bad) {
			t.Errorf("output still contains %q: %q", bad, got)
		}
	}
	for _, keep := range []string{"harmless", "RED", "\t", "\nline2", "é 日本"} {
		if !strings.Contains(got, keep) {
			t.Errorf("output lost %q: %q", keep, got)
		}
	}
}

// End to end: a title with escapes must not reach the terminal through the real command path.
func TestMainNeverPrintsControlCharactersFromTheLedger(t *testing.T) {
	be := &fakeBackend{}
	connect := func(_ context.Context, _, _ string) (Backend, error) { return &evilBackend{fakeBackend: be}, nil }
	var out, errb bytes.Buffer
	if code := Main([]string{"investigations"}, &out, &errb, func(string) string { return "" }, connect); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "harmless") {
		t.Errorf("the title should still be shown: %q", out.String())
	}
}

type evilBackend struct{ *fakeBackend }

func (e *evilBackend) Investigations(context.Context, int) ([]Investigation, error) {
	return []Investigation{{ID: 3, Who: "alice", Title: "harmless\x1b[2J\x1b]0;PWNED\x07 title", SQL: "select 1"}}, nil
}
