package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/cli"
	"github.com/abdul-hamid-achik/monitor/internal/reload"
	uiv2 "github.com/abdul-hamid-achik/monitor/internal/ui/v2"
)

// pprofAddr, when set via --pprof, exposes monitor's OWN net/http/pprof
// endpoints so the profiler / blast-radius features can target monitor itself
// (and for debugging). Most useful with long-running modes (TUI, watch,
// history record, mcp serve).
var pprofAddr string

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
	reloadServer.addr = reload.DefaultAddr
	os.Args = append([]string{os.Args[0]}, extractGlobalFlags(os.Args[1:])...)
}

// startPprofIfEnabled serves monitor's own pprof endpoints when --pprof is set.
// It uses a dedicated mux (not the process-global DefaultServeMux) and an
// http.Server with a ReadHeaderTimeout, and warns when bound to a non-loopback
// address since /debug/pprof/cmdline exposes the process argv.
func startPprofIfEnabled() {
	if pprofAddr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	if host, _, err := net.SplitHostPort(pprofAddr); err == nil && !isLoopback(host) {
		fmt.Fprintf(os.Stderr, "monitor: warning: --pprof on non-loopback %s exposes /debug/pprof (incl. cmdline argv)\n", pprofAddr)
	}
	srv := &http.Server{Addr: pprofAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "monitor: --pprof failed on %s: %v\n", pprofAddr, err)
		}
	}()
	fmt.Fprintf(os.Stderr, "monitor: pprof listening on http://%s/debug/pprof/\n", pprofAddr)
}

// isLoopback reports whether host (the host portion of an addr, "" for a
// wildcard bind like ":6060") refers only to the loopback interface.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func main() {
	startPprofIfEnabled()
	if shouldLaunchTUI(os.Args[1:]) {
		runTUI()
		return
	}
	// cli.Version is set at link time by goreleaser's
	// -X .../internal/cli.Version=<tag> ldflag; "dev" for plain `go build`.
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
	// Derive the known-subcommand set from the registry so dispatch can
	// never drift from the AddCommand list in internal/cli/root.go.
	for _, name := range cli.SubcommandNames() {
		if first == name {
			return false
		}
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

// extractGlobalFlags scans the FULL argument tail for monitor's
// position-independent global flags — --pprof, --reload-addr (value-taking)
// and --reload-server (bool) — setting their package vars and returning the
// remaining args for cobra. Unlike flag.Parse it does not stop at the first
// non-flag token, so `monitor watch --pprof :6060` is honored (the documented
// primary use) rather than silently dropped. Supports --flag=value and
// --flag value; everything after a bare "--" is passed through verbatim so
// cobra's positional separator (e.g. `monitor vault -- cmd`) still works.
func extractGlobalFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		name, value, hasValue := splitFlag(a)
		switch name {
		case "pprof":
			if hasValue {
				pprofAddr = value
			} else if i+1 < len(args) {
				pprofAddr = args[i+1]
				i++
			}
		case "reload-addr":
			if hasValue {
				reloadServer.addr = value
			} else if i+1 < len(args) {
				reloadServer.addr = args[i+1]
				i++
			}
		case "reload-server":
			// Bool flag: bare form enables; --reload-server=false disables.
			reloadServer.enabled = !hasValue || (value == "true" || value == "1")
		default:
			out = append(out, a)
		}
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
