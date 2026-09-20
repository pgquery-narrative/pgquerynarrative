package pqncli

import (
	"io"
	"unicode/utf8"
)

// safeWriter removes terminal control characters from everything the tool prints. Titles, notes,
// statements and recorded proof text are chosen by the people who write them, and an administrator
// reads them in a terminal: a raw ESC could clear the screen, hide rows, forge output lines or set
// the window title. C0 controls (except newline and tab), DEL and the C1 range are replaced by '?'.
// JSON output already escapes them, so it passes through unchanged.
type safeWriter struct{ w io.Writer }

func (s safeWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRune(p[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			out = append(out, p[i]) // not valid UTF-8 (or a split rune): leave it to the terminal
		case r == '\n' || r == '\t':
			out = append(out, byte(r))
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			out = append(out, '?')
		default:
			out = append(out, p[i:i+size]...)
		}
		i += size
	}
	if _, err := s.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
