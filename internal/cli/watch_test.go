package cli

import (
	"bufio"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/notify"
)

func TestWatchAlertCooldownFlag(t *testing.T) {
	flag := newWatchCmd().Flags().Lookup("alert-cooldown")
	if flag == nil {
		t.Fatal("watch command is missing --alert-cooldown")
	}
	if flag.DefValue != defaultAlertCooldown.String() {
		t.Fatalf("--alert-cooldown default = %q, want %q", flag.DefValue, defaultAlertCooldown)
	}
}

func TestDeliveryLimiterBoundsInflight(t *testing.T) {
	limiter := newDeliveryLimiter(1)
	started := make(chan struct{})
	release := make(chan struct{})
	if !limiter.submit(func() {
		close(started)
		<-release
	}) {
		t.Fatal("first delivery should be accepted")
	}
	<-started
	var ran atomic.Bool
	if limiter.submit(func() { ran.Store(true) }) {
		t.Fatal("second delivery should be rejected while the only slot is occupied")
	}
	close(release)
	limiter.wait()
	if ran.Load() {
		t.Fatal("rejected delivery unexpectedly ran")
	}
}

func TestAlertCooldownGatePerPID(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	gate := newAlertCooldownGate(time.Minute)
	first := collector.Alert{Rule: "rss_growth", PID: 42, Detail: "RSS +50 KB/sample"}
	changedDetail := collector.Alert{Rule: "rss_growth", PID: 42, Detail: "RSS +75 KB/sample"}
	otherPID := collector.Alert{Rule: "rss_growth", PID: 43, Detail: "RSS +75 KB/sample"}

	if !gate.allow(first, base) {
		t.Fatal("first alert should pass")
	}
	if gate.allow(changedDetail, base.Add(30*time.Second)) {
		t.Fatal("same rule/PID should be suppressed even when detail changes")
	}
	if !gate.allow(otherPID, base.Add(30*time.Second)) {
		t.Fatal("a different PID should have an independent cooldown")
	}
	if !gate.allow(changedDetail, base.Add(time.Minute)) {
		t.Fatal("alert should pass again when the cooldown expires")
	}
}

func TestAlertCooldownGatePerFilesystem(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	gate := newAlertCooldownGate(time.Minute)
	root91 := collector.Alert{Rule: "disk_fill", Detail: "/ at 91% (>= 90%)"}
	root95 := collector.Alert{Rule: "disk_fill", Detail: "/ at 95% (>= 90%)"}
	data := collector.Alert{Rule: "disk_fill", Detail: "/Volumes/data at home at 92% (>= 90%)"}

	if !gate.allow(root91, base) {
		t.Fatal("first root filesystem alert should pass")
	}
	if gate.allow(root95, base.Add(10*time.Second)) {
		t.Fatal("same filesystem should be suppressed when only usage changes")
	}
	if !gate.allow(data, base.Add(10*time.Second)) {
		t.Fatal("a different filesystem should have an independent cooldown")
	}
	if got := alertIdentity(data); got != "disk_fill|filesystem:/Volumes/data at home" {
		t.Fatalf("filesystem identity = %q", got)
	}
}

func TestAlertCooldownGateSystemRulesAndDisabled(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	cpu := collector.Alert{Rule: "cpu_threshold", Detail: "CPU 91%"}
	memory := collector.Alert{Rule: "mem_threshold", Detail: "memory 91%"}
	gate := newAlertCooldownGate(time.Minute)

	if !gate.allow(cpu, base) || !gate.allow(memory, base) {
		t.Fatal("different system-wide rules should have independent cooldowns")
	}
	if gate.allow(cpu, base.Add(time.Second)) {
		t.Fatal("repeated system-wide rule should be suppressed")
	}

	disabled := newAlertCooldownGate(0)
	if !disabled.allow(cpu, base) || !disabled.allow(cpu, base.Add(time.Second)) {
		t.Fatal("zero cooldown should disable suppression")
	}
}

func TestEmitAlertsCooldownGatesOutputAndSinks(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	gate := newAlertCooldownGate(time.Minute)
	alert := collector.Alert{Rule: "disk_fill", Detail: "/ at 91% (>= 90%)"}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
		_ = writer.Close()
		_ = reader.Close()
	}()

	deliveries := 0
	handlers := []watchAlertHandler{
		func(collector.Event, collector.Alert, *incidents.Diagnosis) { deliveries++ },
	}
	if err := emitAlerts(collector.Event{Timestamp: base}, []collector.Alert{alert}, gate, handlers); err != nil {
		t.Fatalf("first emitAlerts: %v", err)
	}
	alert.Detail = "/ at 95% (>= 90%)"
	if err := emitAlerts(collector.Event{Timestamp: base.Add(time.Second)}, []collector.Alert{alert}, gate, handlers); err != nil {
		t.Fatalf("repeated emitAlerts: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout

	lines := 0
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != 1 {
		t.Fatalf("NDJSON alert lines = %d, want 1", lines)
	}
	if deliveries != 1 {
		t.Fatalf("sink deliveries = %d, want 1", deliveries)
	}
}

func TestEventToSystemInfoPreservesIncidentEvidence(t *testing.T) {
	ts := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	ev := collector.Event{
		Timestamp: ts,
		Hostname:  "host-x",
		CPU:       collector.CPUInfo{UsagePercent: 42},
		Memory:    collector.MemoryInfo{UsagePercent: 55},
		Network:   collector.NetworkInfo{BytesRecvPerSec: 99},
		Disk: collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{
			{MountPoint: "/", UsagePercent: 94},
		}},
		Processes: []collector.ProcessInfo{
			{PID: 42, Name: "leaky", Memory: 512 << 20},
		},
	}

	info := eventToSystemInfo(ev)
	if !reflect.DeepEqual(info.Disk, ev.Disk) {
		t.Errorf("Disk evidence = %+v, want %+v", info.Disk, ev.Disk)
	}
	if !reflect.DeepEqual(info.Processes, ev.Processes) {
		t.Errorf("Processes evidence = %+v, want %+v", info.Processes, ev.Processes)
	}
	if !info.ProcessesLastUpdate.Equal(ts) {
		t.Errorf("ProcessesLastUpdate = %v, want %v", info.ProcessesLastUpdate, ts)
	}
	if !info.LastUpdate.Equal(ts) {
		t.Errorf("LastUpdate = %v, want %v", info.LastUpdate, ts)
	}
}

// TestToNotifyDiagnosis verifies the incidents.Diagnosis -> notify.Diagnosis
// field copy, including the nil passthrough.
func TestToNotifyDiagnosis(t *testing.T) {
	if got := toNotifyDiagnosis(nil); got != nil {
		t.Errorf("toNotifyDiagnosis(nil) = %+v, want nil", got)
	}

	in := &incidents.Diagnosis{
		Summary:     "RSS grew 42%/10min",
		Evidence:    []string{"slope 3.2MB/min"},
		Confidence:  "high",
		NextActions: []string{"monitor_profile_capture type:heap"},
	}
	want := &notify.Diagnosis{
		Summary:     in.Summary,
		Evidence:    in.Evidence,
		Confidence:  in.Confidence,
		NextActions: in.NextActions,
	}
	got := toNotifyDiagnosis(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("toNotifyDiagnosis(%+v) = %+v, want %+v", in, got, want)
	}
}

// TestDiagnosisOfNilSafe verifies diagnosisOf never panics and correctly
// mirrors a collector.Alert's Diagnosis (Sprint 4.1 has landed: collector.Alert
// carries a Diagnosis field).
func TestDiagnosisOfNilSafe(t *testing.T) {
	if got := diagnosisOf(collector.Alert{Rule: "x"}); got != nil {
		t.Errorf("diagnosisOf(no diagnosis) = %+v, want nil", got)
	}

	a := collector.Alert{
		Rule: "rss_growth",
		Diagnosis: &collector.Diagnosis{
			Summary:     "RSS grew 42%/10min",
			Evidence:    []string{"slope 3.2MB/min"},
			Confidence:  "high",
			NextActions: []string{"monitor_profile_capture type:heap"},
		},
	}
	got := diagnosisOf(a)
	if got == nil {
		t.Fatal("diagnosisOf should mirror a non-nil Diagnosis")
	}
	if got.Summary != a.Diagnosis.Summary || got.Confidence != a.Diagnosis.Confidence {
		t.Errorf("diagnosisOf(%+v) = %+v, want a field-for-field mirror", a.Diagnosis, got)
	}
}

func TestWatchRejectsNonPositiveInterval(t *testing.T) {
	cmd := newWatchCmd()
	cmd.SetArgs([]string{"--interval", "0", "--once"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--interval must be greater than zero") {
		t.Fatalf("error = %v, want interval validation", err)
	}
}
