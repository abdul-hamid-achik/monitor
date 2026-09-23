package sourcemap

import "testing"

// TestDecodeVLQValue uses the reference test vectors from the Source Map v3
// spec / the Closure Compiler Base64 VLQ encoder (0, +/-1, +/-2, +/-16, 123):
// each value's Base64 VLQ encoding is unambiguous, so these strings must
// decode back to exactly the listed integer.
func TestDecodeVLQValue(t *testing.T) {
	tests := []struct {
		encoded string
		want    int
	}{
		{"A", 0},
		{"C", 1},
		{"D", -1},
		{"E", 2},
		{"F", -2},
		{"gB", 16},
		{"hB", -16},
		{"2H", 123},
	}
	for _, tt := range tests {
		got, next, err := decodeVLQValue(tt.encoded, 0)
		if err != nil {
			t.Fatalf("decodeVLQValue(%q): unexpected error: %v", tt.encoded, err)
		}
		if got != tt.want {
			t.Errorf("decodeVLQValue(%q) = %d, want %d", tt.encoded, got, tt.want)
		}
		if next != len(tt.encoded) {
			t.Errorf("decodeVLQValue(%q) consumed %d bytes, want %d", tt.encoded, next, len(tt.encoded))
		}
	}
}

// TestDecodeVLQValueSequence decodes several values back to back from one
// string, which is how decodeVLQSegment consumes a mappings segment: there
// is no separator between fields, only the continuation bit says where one
// value ends and the next begins.
func TestDecodeVLQValueSequence(t *testing.T) {
	s := "ACD" // 0, 1, -1 concatenated
	want := []int{0, 1, -1}
	pos := 0
	for i, w := range want {
		got, next, err := decodeVLQValue(s, pos)
		if err != nil {
			t.Fatalf("value %d: unexpected error: %v", i, err)
		}
		if got != w {
			t.Errorf("value %d = %d, want %d", i, got, w)
		}
		pos = next
	}
	if pos != len(s) {
		t.Errorf("consumed %d of %d bytes", pos, len(s))
	}
}

func TestDecodeVLQValueErrors(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if _, _, err := decodeVLQValue("", 0); err == nil {
			t.Fatal("expected an error for an empty string")
		}
	})
	t.Run("invalid character", func(t *testing.T) {
		if _, _, err := decodeVLQValue("!", 0); err == nil {
			t.Fatal("expected an error for a non-base64 character")
		}
	})
	t.Run("truncated continuation", func(t *testing.T) {
		// 'g' has its continuation bit set, so a lone 'g' is truncated.
		if _, _, err := decodeVLQValue("g", 0); err == nil {
			t.Fatal("expected an error for a truncated VLQ value")
		}
	})
	t.Run("too long", func(t *testing.T) {
		// Every character below has its continuation bit set (0x20), so
		// this never terminates within the allowed group count.
		long := "gggggggg"
		if _, _, err := decodeVLQValue(long, 0); err == nil {
			t.Fatal("expected an error for an over-long VLQ value")
		}
	})
}

func TestDecodeVLQSegment(t *testing.T) {
	t.Run("empty segment", func(t *testing.T) {
		values, err := decodeVLQSegment("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(values) != 0 {
			t.Errorf("got %v, want no values", values)
		}
	})
	t.Run("four zero fields", func(t *testing.T) {
		values, err := decodeVLQSegment("AAAA")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []int{0, 0, 0, 0}
		if len(values) != len(want) {
			t.Fatalf("got %v, want %v", values, want)
		}
		for i := range want {
			if values[i] != want[i] {
				t.Errorf("field %d = %d, want %d", i, values[i], want[i])
			}
		}
	})
	t.Run("five fields with a name index", func(t *testing.T) {
		// "AAAAC" = 0,0,0,0,1: a segment with source+name info.
		values, err := decodeVLQSegment("AAAAC")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []int{0, 0, 0, 0, 1}
		if len(values) != len(want) {
			t.Fatalf("got %v, want %v", values, want)
		}
		for i := range want {
			if values[i] != want[i] {
				t.Errorf("field %d = %d, want %d", i, values[i], want[i])
			}
		}
	})
	t.Run("propagates a decode error", func(t *testing.T) {
		if _, err := decodeVLQSegment("A!A"); err == nil {
			t.Fatal("expected an error for an invalid character mid-segment")
		}
	})
}
