package stacktrace

import "strings"

// ansiRunes are the byte values StripANSI treats as starting an escape
// sequence it should remove. Only CSI (ESC '[') and OSC (ESC ']') style
// sequences appear in the runtime output this package parses (Deno's
// colorized panic banner, zap's colorized console encoder), so StripANSI
// only needs to understand those two, plus the bare ESC-prefixed single
// character forms.
const ansiEscape = '\x1b'

// StripANSI removes ANSI/VT100 escape sequences (SGR color codes, and the
// occasional OSC sequence) from s, leaving the visible text untouched.
// Parsers call this defensively on every block, so a caller that already
// stripped ANSI per-line (as the CLI does) pays only the cost of a scan
// with no escape bytes found.
func StripANSI(s string) string {
	if !strings.ContainsRune(s, ansiEscape) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != ansiEscape {
			b.WriteByte(c)
			i++
			continue
		}
		// c == ESC. Look at the next byte to figure out which kind of
		// sequence this is.
		if i+1 >= len(s) {
			// Trailing lone ESC; drop it.
			i++
			continue
		}
		switch s[i+1] {
		case '[':
			// CSI: ESC '[' <params/intermediates> <final byte 0x40-0x7E>.
			j := i + 2
			for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume the final byte
			}
			i = j
		case ']':
			// OSC: ESC ']' ... terminated by BEL or ESC '\'.
			j := i + 2
			for j < len(s) {
				if s[j] == '\a' {
					j++
					break
				}
				if s[j] == ansiEscape && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			// Two-byte escape (e.g. ESC 'c'); drop both bytes.
			i += 2
		}
	}
	return b.String()
}
