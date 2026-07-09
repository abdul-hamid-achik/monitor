package profiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileVerifyArtifact(t *testing.T) {
	nonEmptyFile := filepath.Join(t.TempDir(), "cpu.pb.gz")
	if err := os.WriteFile(nonEmptyFile, []byte("some bytes"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	emptyFile := filepath.Join(t.TempDir(), "empty.pb.gz")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatalf("write empty temp file: %v", err)
	}
	missingFile := filepath.Join(t.TempDir(), "missing.pb.gz")

	tests := []struct {
		name          string
		profile       Profile
		wantVerified  bool
		wantSize      int64
		limitationHas []string
	}{
		{
			name:         "text_only",
			profile:      Profile{Text: "heap profile: 1"},
			wantVerified: true,
			wantSize:     int64(len("heap profile: 1")),
		},
		{
			name:         "symbols_only",
			profile:      Profile{Symbols: []Symbol{{Func: "main.f", File: "f.go", Line: 1}}},
			wantVerified: true,
		},
		{
			name:         "path_non_empty_file",
			profile:      Profile{Path: nonEmptyFile},
			wantVerified: true,
			wantSize:     int64(len("some bytes")),
		},
		{
			name:          "path_missing_file",
			profile:       Profile{Path: missingFile},
			wantVerified:  false,
			limitationHas: []string{missingFile},
		},
		{
			name:          "path_empty_file",
			profile:       Profile{Path: emptyFile},
			wantVerified:  false,
			limitationHas: []string{"empty"},
		},
		{
			name:          "zero_profile",
			profile:       Profile{PID: 9, Type: ProfileHeap},
			wantVerified:  false,
			limitationHas: []string{"heap", "9"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tt.profile.VerifyArtifact()
			if rec.Verified != tt.wantVerified {
				t.Errorf("Verified = %v, want %v (limitation=%q)", rec.Verified, tt.wantVerified, rec.Limitation)
			}
			if tt.wantSize != 0 && rec.SizeBytes != tt.wantSize {
				t.Errorf("SizeBytes = %d, want %d", rec.SizeBytes, tt.wantSize)
			}
			for _, want := range tt.limitationHas {
				if !strings.Contains(rec.Limitation, want) {
					t.Errorf("limitation %q does not contain %q", rec.Limitation, want)
				}
			}
		})
	}
}

// TestReceiptJSONFieldNames locks in the snake_case JSON contract for Receipt.
func TestReceiptJSONFieldNames(t *testing.T) {
	rec := Receipt{Verified: true, SizeBytes: 42}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"verified"`, `"size_bytes"`} {
		if !strings.Contains(s, want) {
			t.Errorf("json %s missing field %s", s, want)
		}
	}
}
