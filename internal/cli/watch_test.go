package cli

import (
	"reflect"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/notify"
)

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
