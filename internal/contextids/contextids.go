// Package contextids collects optional correlation identifiers that attach
// local forensic work (investigate, stash, logs) to an external run —
// typically a Chalupa environment/deployment or a glyphrun MONITOR_RUN_DIR.
//
// These IDs are NEVER mixed into monitor.telemetry_window V1 (privacy lane).
// They only appear on the local diagnostic plane and fcheap incident tags.
package contextids

import (
	"os"
	"path/filepath"
	"strings"
)

// IDs is the correlation envelope. Empty fields are omitted from JSON and
// skipped when building fcheap tags.
type IDs struct {
	Environment  string `json:"environment,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	StepID       string `json:"step_id,omitempty"`
	Suite        string `json:"suite,omitempty"`
	Attempt      string `json:"attempt,omitempty"`
	Release      string `json:"release,omitempty"`
	Service      string `json:"service,omitempty"`
	GitSHA       string `json:"git_sha,omitempty"`
}

// FromEnv reads standard MONITOR_* / CHALUPA_* variables. Explicit non-empty
// fields on override win over the environment.
func FromEnv(override IDs) IDs {
	out := IDs{
		Environment:  first(override.Environment, os.Getenv("MONITOR_ENVIRONMENT"), os.Getenv("CHALUPA_CI_ENVIRONMENT"), os.Getenv("CHALUPA_ENV"), os.Getenv("CHALUPA_ENVIRONMENT")),
		DeploymentID: first(override.DeploymentID, os.Getenv("MONITOR_DEPLOYMENT_ID"), os.Getenv("CHALUPA_DEPLOYMENT_ID")),
		RunID:        first(override.RunID, os.Getenv("MONITOR_RUN_ID"), os.Getenv("CHALUPA_CI_RUN_ID"), os.Getenv("CHALUPA_RUN_ID"), runDirBasename(os.Getenv("MONITOR_RUN_DIR"))),
		StepID:       first(override.StepID, os.Getenv("MONITOR_STEP_ID"), os.Getenv("CHALUPA_CI_STEP_ID")),
		Suite:        first(override.Suite, os.Getenv("MONITOR_SUITE"), os.Getenv("CHALUPA_CI_SUITE")),
		Attempt:      first(override.Attempt, os.Getenv("MONITOR_ATTEMPT"), os.Getenv("CHALUPA_CI_ATTEMPT")),
		Release:      first(override.Release, os.Getenv("MONITOR_RELEASE"), os.Getenv("CHALUPA_RELEASE")),
		Service:      first(override.Service, os.Getenv("MONITOR_SERVICE"), os.Getenv("CHALUPA_SERVICE")),
		GitSHA:       first(override.GitSHA, os.Getenv("MONITOR_GIT_SHA"), os.Getenv("GIT_SHA"), os.Getenv("GITHUB_SHA")),
	}
	return out
}

// Tags returns fcheap-safe tags for the non-empty fields.
func (id IDs) Tags() []string {
	var tags []string
	add := func(prefix, v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		// Tags must stay short and shell-safe; collapse whitespace.
		v = strings.Join(strings.Fields(v), "-")
		runes := []rune(v)
		if len(runes) > 80 {
			v = string(runes[:80])
		}
		tags = append(tags, prefix+v)
	}
	add("env:", id.Environment)
	add("deploy:", id.DeploymentID)
	add("run:", id.RunID)
	add("step:", id.StepID)
	add("suite:", id.Suite)
	add("attempt:", id.Attempt)
	add("release:", id.Release)
	add("service:", id.Service)
	add("git:", id.GitSHA)
	return tags
}

// Empty reports whether every field is blank.
func (id IDs) Empty() bool {
	return id.Environment == "" && id.DeploymentID == "" && id.RunID == "" &&
		id.StepID == "" && id.Suite == "" && id.Attempt == "" &&
		id.Release == "" && id.Service == "" && id.GitSHA == ""
}

func first(vals ...string) string {
	for _, v := range vals {
		if s := cleanID(v); s != "" {
			return s
		}
	}
	return ""
}

func cleanID(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > 256 {
		value = string(runes[:256])
	}
	return value
}

func runDirBasename(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.Base(dir)
}
