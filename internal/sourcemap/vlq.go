// Package sourcemap decodes Source Map v3 documents (the format emitted by
// tsc, bun build, webpack, esbuild and friends) with no external dependency,
// and resolves a generated file:line:col back to its original source
// position.
package sourcemap

import "fmt"

// base64Chars is the alphabet used by the Base64 VLQ encoding that Source
// Map v3's "mappings" field is built from. It intentionally differs from
// standard base64 only in that it has no padding character.
const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// base64Decode maps a byte to its 6-bit value in base64Chars, or -1 if the
// byte is not part of the alphabet.
var base64Decode [256]int8

func init() {
	for i := range base64Decode {
		base64Decode[i] = -1
	}
	for i := 0; i < len(base64Chars); i++ {
		base64Decode[base64Chars[i]] = int8(i)
	}
}

const (
	vlqBaseShift       = 5
	vlqBaseMask        = (1 << vlqBaseShift) - 1 // 0x1F: the 5 data bits
	vlqContinuationBit = 1 << vlqBaseShift       // 0x20: "more groups follow"
	// vlqMaxGroups bounds how many 6-bit groups a single value may span.
	// A 32-bit value needs at most ceil(32/5) = 7 groups; anything longer
	// is malformed input, not a value we should keep shifting into.
	vlqMaxGroups = 7
)

// decodeVLQValue decodes one Base64 VLQ signed integer starting at s[pos].
// It returns the value and the index of the first byte after it.
//
// Encoding recap: the value is written sign-and-magnitude, LSB group first.
// The least significant bit of the *first* group (after removing the
// continuation bit) is the sign bit; the rest is the magnitude, 5 bits per
// group, continuation bit (0x20) set on every group but the last.
func decodeVLQValue(s string, pos int) (int, int, error) {
	start := pos
	result := 0
	shift := uint(0)
	groups := 0
	for {
		if pos >= len(s) {
			return 0, pos, fmt.Errorf("sourcemap: truncated VLQ value at offset %d", start)
		}
		c := s[pos]
		digit := base64Decode[c]
		if digit < 0 {
			return 0, pos, fmt.Errorf("sourcemap: invalid VLQ character %q at offset %d", c, pos)
		}
		pos++
		groups++
		if groups > vlqMaxGroups {
			return 0, pos, fmt.Errorf("sourcemap: VLQ value too long starting at offset %d", start)
		}
		cont := digit&vlqContinuationBit != 0
		result += int(digit&vlqBaseMask) << shift
		shift += vlqBaseShift
		if !cont {
			break
		}
	}
	if result&1 != 0 {
		result = -(result >> 1)
	} else {
		result = result >> 1
	}
	return result, pos, nil
}

// decodeVLQSegment decodes every value packed back-to-back into one
// "mappings" segment (the text between two commas, or a comma and a
// semicolon). A segment carries 1, 4 or 5 fields per the Source Map v3
// spec; decodeVLQSegment does not itself enforce that count, so callers can
// give a precise per-field error message.
func decodeVLQSegment(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var values []int
	pos := 0
	for pos < len(s) {
		v, next, err := decodeVLQValue(s, pos)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		pos = next
	}
	return values, nil
}
