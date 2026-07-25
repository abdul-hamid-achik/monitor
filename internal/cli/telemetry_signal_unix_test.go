//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/telemetry"
)

const telemetrySignalHelperEnv = "MONITOR_TELEMETRY_SIGNAL_HELPER"

func TestTelemetrySIGTERMHelperProcess(t *testing.T) {
	if os.Getenv(telemetrySignalHelperEnv) != "1" {
		return
	}
	cmd := newTelemetryCmd()
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"--interval", "1s", "--window", "30s"})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTelemetryCommandSIGTERMFlushesPartialWindow(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestTelemetrySIGTERMHelperProcess$")
	command.Env = append(os.Environ(), telemetrySignalHelperEnv+"=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	// This is deliberately a real signal-path test. The unit suite uses an
	// injected monotonic clock; this one allows one production 1s sample before
	// exercising Cobra's signal.NotifyContext wiring.
	time.Sleep(2500 * time.Millisecond)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper exit: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("telemetry command did not exit after SIGTERM")
	}

	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("stdout contains %d lines, want one NDJSON window:\n%s", len(lines), stdout.String())
	}
	var envelope telemetry.WindowEnvelope
	if err := json.Unmarshal(lines[0], &envelope); err != nil {
		t.Fatalf("decode partial window: %v\n%s", err, lines[0])
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate partial window: %v", err)
	}
	if !envelope.Window.Partial || envelope.Window.SampleCount < 1 {
		t.Fatalf("SIGTERM window = %+v, want a non-empty partial window", envelope.Window)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"hostname"`), []byte(`"pid"`), []byte(`"process"`),
		[]byte(`"path"`), []byte(`"mount_point"`), []byte(`"device"`),
		[]byte(`"reason"`), []byte(`"detail"`), []byte(`"diagnosis"`),
	} {
		if bytes.Contains(lines[0], forbidden) {
			t.Errorf("telemetry output contains forbidden key %s", forbidden)
		}
	}
}
