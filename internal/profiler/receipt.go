package profiler

import (
	"fmt"
	"os"
)

// Receipt is the artifact-exists verification for a captured Profile: proof
// the capture produced usable evidence (non-empty file, or text/symbols)
// before any surface reports success.
type Receipt struct {
	Verified   bool   `json:"verified"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Limitation string `json:"limitation,omitempty"`
}

// VerifyArtifact checks the profile's evidence. Path-backed profiles (cpu)
// must exist on disk and be non-empty; in-memory profiles (heap/goroutine/
// sample) must carry text or parsed symbols.
func (p Profile) VerifyArtifact() Receipt {
	if p.Path != "" {
		fi, err := os.Stat(p.Path)
		if err != nil {
			return Receipt{Limitation: fmt.Sprintf("profile file missing at %s: %v", p.Path, err)}
		}
		if fi.Size() == 0 {
			return Receipt{Limitation: fmt.Sprintf("profile file is empty at %s", p.Path)}
		}
		return Receipt{Verified: true, SizeBytes: fi.Size()}
	}
	if len(p.Text) > 0 || len(p.Symbols) > 0 {
		return Receipt{Verified: true, SizeBytes: int64(len(p.Text))}
	}
	return Receipt{Limitation: fmt.Sprintf("%s profile of pid %d contains no data (no text, symbols, or file)", p.Type, p.PID)}
}
