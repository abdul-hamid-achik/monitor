package cli

import (
	"os"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/devrun"
)

// A pid whose inspector was opened through NODE_OPTIONS by `monitor run
// --inspect` has no --inspect in its argv; the launch registry is the only
// record of its port, and a raw-pid capture must find it there.
func TestRegisteredInspectorForFindsLaunchByLeafPID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	self := int32(os.Getpid())
	if _, ok := registeredInspectorFor(self); ok {
		t.Fatal("an empty registry must not resolve an inspector")
	}
	if _, err := devrun.WriteRegistryEntry(devrun.RegistryEntry{
		Schema: devrun.RegistrySchema, LaunchID: "L-1", PID: int(self), Name: "api", Project: "shop",
		StartedAt: time.Now(), Scan: "stderr",
		Inspectors: []devrun.RegistryInspector{{PID: self, Port: 9339, WS: "ws://127.0.0.1:9339/" + "secret-uuid"}},
	}); err != nil {
		t.Fatal(err)
	}
	addr, ok := registeredInspectorFor(self)
	if !ok || addr != "127.0.0.1:9339" {
		t.Fatalf("registeredInspectorFor = %q, %v; want 127.0.0.1:9339", addr, ok)
	}
	if _, ok := registeredInspectorFor(self + 1); ok {
		t.Fatal("another pid must not borrow this launch's inspector")
	}
}
