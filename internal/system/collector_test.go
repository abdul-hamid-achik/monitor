package system

import (
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    uint64
		expected string
	}{
		{0, "00 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, tt := range tests {
		result := FormatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatBytes(%d) = %s, expected %s", tt.bytes, result, tt.expected)
		}
	}
}

func TestFormatUint64(t *testing.T) {
	tests := []struct {
		n        uint64
		expected string
	}{
		{0, "00"},
		{5, "05"},
		{42, "42"},
		{1000, "1000"},
		{999999, "999999"},
	}

	for _, tt := range tests {
		result := formatUint64(tt.n)
		if result != tt.expected {
			t.Errorf("formatUint64(%d) = %s, expected %s", tt.n, result, tt.expected)
		}
	}
}
