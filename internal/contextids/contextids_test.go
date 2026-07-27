package contextids

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFromEnvOverrideWins(t *testing.T) {
	clearCorrelationEnv(t)
	t.Setenv("MONITOR_ENVIRONMENT", "from-env")
	t.Setenv("CHALUPA_DEPLOYMENT_ID", "dep-1")
	t.Setenv("MONITOR_RUN_DIR", "/tmp/runs/run-abc")

	got := FromEnv(IDs{Environment: "override-env", RunID: "explicit-run"})
	if got.Environment != "override-env" {
		t.Errorf("Environment = %q", got.Environment)
	}
	if got.DeploymentID != "dep-1" {
		t.Errorf("DeploymentID = %q", got.DeploymentID)
	}
	if got.RunID != "explicit-run" {
		t.Errorf("RunID = %q", got.RunID)
	}
}

func TestFromEnvRunDirBasename(t *testing.T) {
	clearCorrelationEnv(t)
	t.Setenv("MONITOR_RUN_DIR", "/var/monitor/runs/job-42")
	t.Setenv("MONITOR_RUN_ID", "")
	got := FromEnv(IDs{})
	if got.RunID != "job-42" {
		t.Fatalf("RunID = %q want job-42", got.RunID)
	}
}

func TestFromEnvChalupaCIContract(t *testing.T) {
	clearCorrelationEnv(t)
	t.Setenv("CHALUPA_CI_ENVIRONMENT", "preview-42")
	t.Setenv("CHALUPA_CI_RUN_ID", "run-abc")
	t.Setenv("CHALUPA_CI_STEP_ID", "test")
	t.Setenv("CHALUPA_CI_SUITE", "pull-request")
	t.Setenv("CHALUPA_CI_ATTEMPT", "2")

	got := FromEnv(IDs{})
	if got.Environment != "preview-42" || got.RunID != "run-abc" || got.StepID != "test" || got.Suite != "pull-request" || got.Attempt != "2" {
		t.Fatalf("FromEnv(CHALUPA_CI_*) = %+v", got)
	}
}

func TestIDsAreBoundedAndTagsRemainValidUTF8(t *testing.T) {
	clearCorrelationEnv(t)
	value := strings.Repeat("é", 300) + "\nignored-control"
	got := FromEnv(IDs{RunID: value})
	if utf8.RuneCountInString(got.RunID) != 256 || !utf8.ValidString(got.RunID) {
		t.Fatalf("bounded run id is invalid: runes=%d valid=%t", utf8.RuneCountInString(got.RunID), utf8.ValidString(got.RunID))
	}
	tags := got.Tags()
	if len(tags) != 1 || utf8.RuneCountInString(strings.TrimPrefix(tags[0], "run:")) != 80 || !utf8.ValidString(tags[0]) {
		t.Fatalf("tags = %#v", tags)
	}
}

func clearCorrelationEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"MONITOR_ENVIRONMENT", "MONITOR_DEPLOYMENT_ID", "MONITOR_RUN_ID", "MONITOR_RUN_DIR",
		"MONITOR_STEP_ID", "MONITOR_SUITE", "MONITOR_ATTEMPT", "MONITOR_RELEASE",
		"MONITOR_SERVICE", "MONITOR_GIT_SHA", "CHALUPA_CI_ENVIRONMENT", "CHALUPA_CI_RUN_ID",
		"CHALUPA_CI_STEP_ID", "CHALUPA_CI_SUITE", "CHALUPA_CI_ATTEMPT", "CHALUPA_ENV",
		"CHALUPA_ENVIRONMENT", "CHALUPA_DEPLOYMENT_ID", "CHALUPA_RUN_ID", "CHALUPA_RELEASE",
		"CHALUPA_SERVICE", "GIT_SHA", "GITHUB_SHA",
	} {
		t.Setenv(name, "")
	}
}

func TestTags(t *testing.T) {
	tags := IDs{Environment: "ci", DeploymentID: "d1", RunID: "r1", StepID: "test", Suite: "pr", Attempt: "2", Release: "v1.2.3"}.Tags()
	joined := strings.Join(tags, " ")
	for _, want := range []string{"env:ci", "deploy:d1", "run:r1", "step:test", "suite:pr", "attempt:2", "release:v1.2.3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tags %v missing %q", tags, want)
		}
	}
	if !(IDs{}).Empty() {
		t.Fatal("zero IDs should be Empty")
	}
}
