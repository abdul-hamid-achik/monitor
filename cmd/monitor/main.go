package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/abdul-hamid-achik/monitor/internal/cli"
	"github.com/abdul-hamid-achik/monitor/internal/reload"
	uiv2 "github.com/abdul-hamid-achik/monitor/internal/ui/v2"
)

// version is set by goreleaser during build
var version = "dev"

// reloadServer, when set, is started by runTUI() to expose POST /reload
// on 127.0.0.1:7351 (or the configured addr). External processes can
// then trigger an in-process refresh via `monitor reload`. The flag is
// off by default; CI / agent harnesses that need programmatic reload
// turn it on.
var reloadServer struct {
	enabled bool
	addr    string
}

func init() {
	fs := flag.NewFlagSet("monitor", flag.ContinueOnError)
	fs.BoolVar(&reloadServer.enabled, "reload-server", false,
		"start a localhost HTTP /reload endpoint inside the TUI")
	fs.StringVar(&reloadServer.addr, "reload-addr", reload.DefaultAddr,
		"address for the /reload endpoint when --reload-server is set")
	_ = fs.Parse(os.Args[1:])
	os.Args = append([]string{os.Args[0]}, stripParsedFlags(os.Args[1:], "reload-server", "reload-addr")...)
}

func main() {
	os.Args = append([]string{os.Args[0]}, os.Args[1:]...)
	if shouldLaunchTUI(os.Args[1:]) {
		runTUI()
		return
	}
	cli.Version = version
	cli.Execute()
}

func shouldLaunchTUI(args []string) bool {
	if len(args) == 0 {
		return true
	}
	first := args[0]
	if first == "--help" || first == "-h" || first == "help" {
		return false
	}
	if first == "--version" || first == "version" {
		return false
	}
	switch first {
	case "snapshot", "watch", "process", "kill", "profile",
		"investigate", "stash", "incidents", "logs", "doctor",
		"run", "reload", "mcp", "vault", "v2":
		return false
	}
	if len(first) > 0 && first[0] == '-' {
		return false
	}
	return true
}

// runTUI launches the v2 TUI by default.
func runTUI() {
	startReloadServerIfEnabled()
	if err := uiv2.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running monitor TUI: %v\n", err)
		blockOnSignalIfReloadEnabled()
	}
}

// startReloadServerIfEnabled starts the /reload HTTP server if
// --reload-server was passed. Best-effort: logs a warning on failure.
func startReloadServerIfEnabled() {
	if !reloadServer.enabled {
		return
	}
	srv := reload.NewServer(reloadServer.addr, reload.NoopReloader{})
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: --reload-server failed to start on %s: %v\n", reloadServer.addr, err)
	} else {
		fmt.Fprintf(os.Stderr, "monitor: /reload endpoint listening on http://%s\n", srv.Addr())
	}
}

// blockOnSignalIfReloadEnabled blocks on SIGINT/SIGTERM when the
// reload server is enabled, so the server keeps serving in headless
// environments where the TUI can't claim a TTY.
func blockOnSignalIfReloadEnabled() {
	if !reloadServer.enabled {
		return
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

// stripParsedFlags removes occurrences of the named flags (and the
// value that follows them when present) from a flag tail. Lets a
// custom FlagSet share argv with cobra without cobra rejecting the
// flags. Flag names are stored without the leading "--", so we strip
// the prefix before lookup. The "--" arg terminator is also dropped.
func stripParsedFlags(args []string, names ...string) []string {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	out := args[:0]
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--" {
			continue
		}
		name, _, hasValue := splitFlag(a)
		if name != "" && set[name] {
			if !hasValue {
				skipNext = true
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// splitFlag parses a CLI argument into name + value (true when the
// value was joined with "="). Empty name means the argument isn't a
// --flag form.
func splitFlag(arg string) (name, value string, hasValue bool) {
	if len(arg) < 3 || arg[:2] != "--" {
		return "", "", false
	}
	rest := arg[2:]
	for i, c := range rest {
		if c == '=' {
			return rest[:i], rest[i+1:], true
		}
		if c == ' ' {
			return "", "", false
		}
	}
	return rest, "", false
}