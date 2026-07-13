package temperature

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEstimateMonotonicWithCpuLoad(t *testing.T) {
	low := Estimate(0)
	high := Estimate(80)
	// Higher CPU load should produce a higher CPU package estimate.
	if high.CPUPackage <= low.CPUPackage {
		t.Fatalf("CPU package estimate should rise with load; low=%v high=%v",
			low.CPUPackage, high.CPUPackage)
	}
	if low.Source != KindEstimated {
		t.Fatalf("Estimate must tag source as estimated; got %q", low.Source)
	}
	if !low.Available {
		t.Fatalf("Estimate must always set Available=true; got false")
	}
}

func TestParseLineTempVariants(t *testing.T) {
	cases := []struct {
		line   string
		field  string // which Reading field should be populated
		expect float64
		ok     bool
	}{
		{"CPU die temperature: 55.32 C", "CPUCores", 55.32, true},
		{"CPU Package temperature: 58.10 C", "CPUPackage", 58.10, true},
		{"GPU die temperature: 49.00 C", "GPU", 49.00, true},
		{"ANE die temperature: 51.00 C", "ANE", 51.00, true},
		{"Battery temperature: 32.5 C", "Battery", 32.5, true},
		{"Inlet temperature: 24.0 C", "Ambient", 24.0, true},
		{"Ambient temperature: 24.0 C", "Ambient", 24.0, true},
		{"random text: not temperature", "", 0, false},
		{"CPU die temperature: not-a-number C", "", 0, false},
		{"Total CPU Time: 5.0s", "", 0, false},
	}
	for _, tc := range cases {
		r, ok := parseLine(tc.line)
		if ok != tc.ok {
			t.Errorf("parseLine(%q) ok=%v, want %v", tc.line, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		var got float64
		switch tc.field {
		case "CPUPackage":
			got = r.CPUPackage
		case "CPUCores":
			got = r.CPUCores
		case "GPU":
			got = r.GPU
		case "ANE":
			got = r.ANE
		case "Battery":
			got = r.Battery
		case "Ambient":
			got = r.Ambient
		}
		if got != tc.expect {
			t.Errorf("parseLine(%q) %s=%v, want %v", tc.line, tc.field, got, tc.expect)
		}
	}
}

func TestParseLineFan(t *testing.T) {
	r, ok := parseLine("Fan: 1234 rpm")
	if !ok {
		t.Fatalf("Fan line should parse; got ok=false")
	}
	if r.FanRPM != 1234 {
		t.Errorf("FanRPM=%d, want 1234", r.FanRPM)
	}

	r, ok = parseLine("Fan mode: auto")
	if !ok {
		t.Fatalf("Fan mode line should parse; got ok=false")
	}
	if r.FanMode != "auto" {
		t.Errorf("FanMode=%q, want auto", r.FanMode)
	}

	r, ok = parseLine("Fan: not-a-number rpm")
	if ok {
		t.Errorf("garbage fan line should fail; got ok=true, r=%+v", r)
	}
}

func TestNewWithoutSudoFallsBackToEstimate(t *testing.T) {
	// Point both binaries at paths that don't exist on PATH to force the
	// fallback. We use a guaranteed-missing path so the test is hermetic.
	ts := New(context.Background(), Options{
		Bin:  filepath.Join(t.TempDir(), "no-such-powermetrics"),
		SUDO: filepath.Join(t.TempDir(), "no-such-sudo"),
	})
	defer func() {
		if err := ts.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if ts.Started() {
		t.Fatalf("Started() should be false when binaries are missing")
	}
	r := ts.Latest()
	if r.Source != KindEstimated {
		t.Fatalf("Source should be estimated when powermetrics is unavailable; got %q", r.Source)
	}
	if !r.Available {
		t.Fatalf("Reading should always be available (estimated at worst); got Available=false")
	}
}

func TestNewWithFakeBinaryUpgradesToPowermetrics(t *testing.T) {
	// Build a tiny shell script that prints powermetrics-style output and
	// then sleeps briefly so the streaming parser has time to consume it.
	// The fake "sudo" simply exec's its arguments so it transparently
	// passes through to the fake "powermetrics" below — mirroring how the
	// real sudo would invoke /usr/bin/powermetrics.
	script := `#!/bin/sh
echo "CPU die temperature: 61.50 C"
echo "GPU die temperature: 49.25 C"
echo "ANE die temperature: 50.00 C"
sleep 1
`
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-powermetrics")
	if err := writeExecutable(t, bin, script); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	// fake sudo strips its own `-n` flag (real sudo's "non-interactive"
	// flag) and execs the remaining args, so the call shape matches
	// `sudo -n /usr/bin/powermetrics <args>`.
	sudoScript := "#!/bin/sh\nshift\nexec \"$@\"\n"
	sudo := filepath.Join(dir, "fake-sudo")
	if err := writeExecutable(t, sudo, sudoScript); err != nil {
		t.Fatalf("write fake sudo: %v", err)
	}

	var logged []string
	ts := New(context.Background(), Options{
		Bin:  bin,
		SUDO: sudo,
		Logf: func(format string, args ...any) { logged = append(logged, format) },
	})
	defer func() {
		if err := ts.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	// Wait for the streaming goroutine to parse at least one line.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := ts.Latest()
		if r.Source == KindPowermetrics && r.CPUCores > 0 {
			if r.CPUCores != 61.50 {
				t.Errorf("CPUCores=%v, want 61.50", r.CPUCores)
			}
			if r.GPU != 49.25 {
				t.Errorf("GPU=%v, want 49.25", r.GPU)
			}
			if r.ANE != 50.00 {
				t.Errorf("ANE=%v, want 50.00", r.ANE)
			}
			// Regression for the estimate-bleed fix: the fake stream never
			// emits a Battery reading, so it must stay 0 — not carry the
			// Estimate(0) seed (~38) into a Source=powermetrics record.
			if r.Battery != 0 {
				t.Errorf("Battery=%v, want 0 (un-emitted field must not bleed the estimate)", r.Battery)
			}
			// Started() now means "live data flowing", true only after parse.
			if !ts.Started() {
				t.Error("Started() should be true once powermetrics data has arrived")
			}
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Source never upgraded to powermetrics; final reading=%+v logs=%v",
		ts.Latest(), logged)
}

// TestCloseIsIdempotentAndSafe verifies Close() can be called once or
// many times without panicking, and on a fallback Source (no
// subprocess) Close is a no-op.
func TestCloseIsIdempotentAndSafe(t *testing.T) {
	ts := New(context.Background(), Options{
		Bin:  filepath.Join(t.TempDir(), "missing"),
		SUDO: filepath.Join(t.TempDir(), "missing"),
	})
	if err := ts.Close(); err != nil {
		t.Errorf("first Close should not error on fallback Source; got %v", err)
	}
	if err := ts.Close(); err != nil {
		t.Errorf("second Close should not error; got %v", err)
	}
}

// writeExecutable writes a shell script with executable perms.
func writeExecutable(t *testing.T, path, body string) error {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// TestKindValues pins the public string values so JSON consumers can rely
// on them being stable.
func TestKindValues(t *testing.T) {
	if string(KindEstimated) != "estimated" {
		t.Errorf("KindEstimated should serialize as %q; got %q", "estimated", KindEstimated)
	}
	if string(KindPowermetrics) != "powermetrics" {
		t.Errorf("KindPowermetrics should serialize as %q; got %q", "powermetrics", KindPowermetrics)
	}
}
