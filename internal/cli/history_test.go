package cli

import (
	"strings"
	"testing"
)

func TestHistoryRecordRejectsUnsafeIntervalBeforeOpeningStore(t *testing.T) {
	cmd := newHistoryRecordCmd()
	cmd.SetArgs([]string{"--interval", "0", "--db", t.TempDir() + "/history.veclite"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least 100ms") {
		t.Fatalf("error = %v, want full collection interval validation", err)
	}
}
