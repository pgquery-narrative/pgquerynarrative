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
			// Not valid UTF-8 (or a split rune). A byte in the C1 range still reaches the
			// terminal as a raw control byte even without a valid encoding around it, so it
			// gets the same '?' every other C1 byte gets; anything else is left to the terminal.
			if p[i] >= 0x80 && p[i] <= 0x9f {
				out = append(out, '?')
			} else {
				out = append(out, p[i])
			}
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
