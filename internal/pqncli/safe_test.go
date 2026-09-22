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

// A C1 control byte that is not part of a valid UTF-8 sequence (e.g. a lone 0x9b, not the two-byte
// \u009b) must still be replaced: it reaches the terminal as a raw control byte either way.
func TestSafeWriterStripsInvalidUTF8C1Bytes(t *testing.T) {
	var out bytes.Buffer
	in := append([]byte("before"), 0x9b, 0x9d)
	in = append(in, []byte("after")...)
	if _, err := (safeWriter{&out}).Write(in); err != nil {
		t.Fatalf("err=%v", err)
	}
	got := out.Bytes()
	if bytes.ContainsAny(got, "\x9b\x9d") {
		t.Errorf("output still contains a raw C1 byte: %q", got)
	}
	if !bytes.Contains(got, []byte("before")) || !bytes.Contains(got, []byte("after")) {
		t.Errorf("output lost surrounding text: %q", got)
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
