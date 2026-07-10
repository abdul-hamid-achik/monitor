package capability

import (
	"errors"
	"testing"
)

func TestDetectCrossPlatformCapabilities(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/sample", nil }
	missing := func(string) (string, error) { return "", errors.New("missing") }
	tests := []struct {
		name       string
		detector   Detector
		capability Name
		want       State
	}{
		{name: "linux load average", detector: Detector{GOOS: "linux", LookPath: missing}, capability: CPULoadAverage, want: Supported},
		{name: "linux sample", detector: Detector{GOOS: "linux", LookPath: found}, capability: ProfileSample, want: Unsupported},
		{name: "macOS load average", detector: Detector{GOOS: "darwin", LookPath: found}, capability: CPULoadAverage, want: Unsupported},
		{name: "macOS sample installed", detector: Detector{GOOS: "darwin", LookPath: found}, capability: ProfileSample, want: Supported},
		{name: "macOS sample missing", detector: Detector{GOOS: "darwin", LookPath: missing}, capability: ProfileSample, want: Unavailable},
		{name: "unknown system", detector: Detector{GOOS: "plan9", LookPath: missing}, capability: SystemMetrics, want: Unsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect(tt.detector).SupportFor(tt.capability)
			if got.State != tt.want {
				t.Fatalf("state = %q, want %q (%s)", got.State, tt.want, got.Reason)
			}
		})
	}
}

func TestRequireBlocksBeforeUse(t *testing.T) {
	set := Detect(Detector{GOOS: "linux", LookPath: func(string) (string, error) { return "", errors.New("missing") }})
	if err := set.Require(SystemMetrics, ProcessMetrics); err != nil {
		t.Fatalf("supported requirements rejected: %v", err)
	}
	err := set.Require(ProfileSample)
	var capabilityErr *Error
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("Require(sample) error = %T %v, want *Error", err, err)
	}
	if capabilityErr.State != Unsupported {
		t.Fatalf("state = %q, want unsupported", capabilityErr.State)
	}
}
