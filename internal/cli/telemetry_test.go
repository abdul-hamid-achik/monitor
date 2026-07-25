package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTelemetryCommandUsesSafeProductionDefaults(t *testing.T) {
	cmd := newTelemetryCmd()
	for name, want := range map[string]string{
		"interval": "5s",
		"window":   "30s",
		"once":     "false",
	} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Errorf("--%s default = %v, want %q", name, flag, want)
		}
	}
}

func TestTelemetryCommandValidatesWindowAndArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"zero_interval", []string{"--interval", "0", "--window", "1s"}, "interval"},
		{"sub_second_interval", []string{"--interval", "999ms", "--window", "1s"}, "at least 1s"},
		{"short_window", []string{"--interval", "2s", "--window", "1s"}, "window"},
		{"too_many_samples", []string{"--interval", "1s", "--window", "3601s"}, "maximum"},
		{"positional_argument", []string{"unexpected"}, "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newTelemetryCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Execute error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestTelemetryHelpDocumentsMachineOnlyContract(t *testing.T) {
	cmd := newTelemetryCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		"privacy-safe", "--interval", "--window", "--once",
		"environment variables", "durable storage",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help missing %q:\n%s", want, text)
		}
	}
}
