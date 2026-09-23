package devrun

import (
	"strings"
	"testing"
)

func envValue(t *testing.T, environ []string, name string) (string, bool) {
	t.Helper()
	for _, kv := range environ {
		n, v, ok := strings.Cut(kv, "=")
		if ok && n == name {
			return v, true
		}
	}
	return "", false
}

func TestResolveLaunchIDsFreshLaunch(t *testing.T) {
	launch := ResolveLaunchIDs([]string{"PATH=/bin"}, "web-api", "/repo")
	if launch.ID == "" {
		t.Error("ID is empty, want a fresh random id")
	}
	if launch.Service != "web-api" {
		t.Errorf("Service = %q, want web-api", launch.Service)
	}
	if launch.Root != "/repo" {
		t.Errorf("Root = %q, want /repo", launch.Root)
	}
}

func TestResolveLaunchIDsFreshLaunchIDsAreUnique(t *testing.T) {
	a := ResolveLaunchIDs(nil, "svc", "/repo")
	b := ResolveLaunchIDs(nil, "svc", "/repo")
	if a.ID == b.ID {
		t.Errorf("two fresh launches got the same ID %q", a.ID)
	}
}

func TestResolveLaunchIDsNestedInheritsUnchanged(t *testing.T) {
	environ := []string{
		"PATH=/bin",
		"MONITOR_LAUNCH_ID=parent-id-123",
		"MONITOR_LAUNCH_SERVICE=parent-svc",
		"MONITOR_LAUNCH_ROOT=/parent/repo",
	}
	launch := ResolveLaunchIDs(environ, "child-name-should-be-ignored", "/child/repo/should/be/ignored")
	want := LaunchIDs{ID: "parent-id-123", Service: "parent-svc", Root: "/parent/repo"}
	if launch != want {
		t.Errorf("ResolveLaunchIDs (nested) = %+v, want %+v (inherited unchanged)", launch, want)
	}
}

func TestResolveLaunchIDsEmptyLaunchRootIsNotNesting(t *testing.T) {
	// MONITOR_LAUNCH_ROOT="" (present but empty) must not be treated as
	// nesting -- only a genuinely non-empty root signals an outer launch.
	environ := []string{"MONITOR_LAUNCH_ROOT=", "MONITOR_LAUNCH_ID=stale"}
	launch := ResolveLaunchIDs(environ, "svc", "/repo")
	if launch.Root != "/repo" {
		t.Errorf("Root = %q, want /repo (fresh launch, not nested)", launch.Root)
	}
	if launch.ID == "stale" {
		t.Error("a fresh launch must not inherit a stale/empty-rooted MONITOR_LAUNCH_ID")
	}
}

func TestBuildEnvStripsLegacyMonitorVars(t *testing.T) {
	environ := []string{"PATH=/bin", "MONITOR=1", "MONITOR_RUN_DIR=/tmp/rundir", "HOME=/home/me"}
	out := BuildEnv(environ, LaunchIDs{ID: "id1", Service: "svc", Root: "/repo"}, false, false)
	if _, ok := envValue(t, out, "MONITOR"); ok {
		t.Error("MONITOR=1 leaked into the child's environment")
	}
	if _, ok := envValue(t, out, "MONITOR_RUN_DIR"); ok {
		t.Error("MONITOR_RUN_DIR leaked into the child's environment")
	}
	if v, ok := envValue(t, out, "HOME"); !ok || v != "/home/me" {
		t.Errorf("HOME = %q, %v, want /home/me preserved", v, ok)
	}
}

func TestBuildEnvSetsLaunchTriple(t *testing.T) {
	out := BuildEnv(nil, LaunchIDs{ID: "abc", Service: "web-api", Root: "/repo"}, false, false)
	cases := map[string]string{
		EnvLaunchID:      "abc",
		EnvLaunchService: "web-api",
		EnvLaunchRoot:    "/repo",
	}
	for name, want := range cases {
		got, ok := envValue(t, out, name)
		if !ok || got != want {
			t.Errorf("%s = %q, %v, want %q", name, got, ok, want)
		}
	}
}

func TestBuildEnvNeverExportsMonitorOrRunID(t *testing.T) {
	// The naming ADR's central rule: run -- exports ONLY MONITOR_LAUNCH_*.
	environ := []string{"MONITOR_RUN_ID=should-not-appear", "MONITOR_SERVICE=should-not-appear-either"}
	out := BuildEnv(environ, LaunchIDs{ID: "x", Service: "y", Root: "/z"}, false, false)
	for _, kv := range out {
		if strings.HasPrefix(kv, "MONITOR=") || strings.HasPrefix(kv, "MONITOR_RUN_DIR=") {
			t.Errorf("forbidden variable exported: %q", kv)
		}
	}
	// MONITOR_RUN_ID/MONITOR_SERVICE are contextids'/the caller's own
	// business, not run --'s to strip or set -- BuildEnv must pass them
	// through unmodified rather than deleting or overwriting them.
	if v, ok := envValue(t, out, "MONITOR_RUN_ID"); !ok || v != "should-not-appear" {
		t.Errorf("MONITOR_RUN_ID = %q, %v, want passed through unmodified", v, ok)
	}
}

func TestBuildEnvAppendsSourceMapsWithoutOverwriting(t *testing.T) {
	environ := []string{"NODE_OPTIONS=--max-old-space-size=4096"}
	out := BuildEnv(environ, LaunchIDs{}, true, false)
	got, ok := envValue(t, out, "NODE_OPTIONS")
	if !ok {
		t.Fatal("NODE_OPTIONS missing from the child's environment")
	}
	if !strings.Contains(got, "--max-old-space-size=4096") {
		t.Errorf("NODE_OPTIONS = %q, lost the pre-existing value", got)
	}
	if !strings.Contains(got, "--enable-source-maps") {
		t.Errorf("NODE_OPTIONS = %q, missing --enable-source-maps", got)
	}
}

func TestBuildEnvCreatesNodeOptionsWhenUnset(t *testing.T) {
	out := BuildEnv(nil, LaunchIDs{}, true, false)
	got, ok := envValue(t, out, "NODE_OPTIONS")
	if !ok || got != "--enable-source-maps" {
		t.Errorf("NODE_OPTIONS = %q, %v, want exactly --enable-source-maps", got, ok)
	}
}

func TestBuildEnvNoSourceMapsLeavesNodeOptionsAlone(t *testing.T) {
	environ := []string{"NODE_OPTIONS=--max-old-space-size=4096"}
	out := BuildEnv(environ, LaunchIDs{}, false, false)
	got, _ := envValue(t, out, "NODE_OPTIONS")
	if got != "--max-old-space-size=4096" {
		t.Errorf("NODE_OPTIONS = %q, want the original value untouched (--no-source-maps)", got)
	}
	// --no-source-maps must not fabricate NODE_OPTIONS either, when none
	// was set to begin with.
	out2 := BuildEnv(nil, LaunchIDs{}, false, false)
	if _, ok := envValue(t, out2, "NODE_OPTIONS"); ok {
		t.Error("NODE_OPTIONS created even though appendSourceMaps was false")
	}
}

func TestBuildEnvPythonUnbufferedOnlyWhenScanningStdoutAndUnset(t *testing.T) {
	out := BuildEnv(nil, LaunchIDs{}, false, true)
	if v, ok := envValue(t, out, "PYTHONUNBUFFERED"); !ok || v != "1" {
		t.Errorf("PYTHONUNBUFFERED = %q, %v, want 1 when scanning stdout", v, ok)
	}

	out2 := BuildEnv(nil, LaunchIDs{}, false, false)
	if _, ok := envValue(t, out2, "PYTHONUNBUFFERED"); ok {
		t.Error("PYTHONUNBUFFERED set even though stdout is not scanned")
	}

	out3 := BuildEnv([]string{"PYTHONUNBUFFERED=0"}, LaunchIDs{}, false, true)
	if v, _ := envValue(t, out3, "PYTHONUNBUFFERED"); v != "0" {
		t.Errorf("PYTHONUNBUFFERED = %q, want the user's existing 0 preserved, not overwritten", v)
	}
}
