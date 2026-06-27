package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/cli"
)

// pprofAddr, when set via --pprof, exposes monitor's OWN net/http/pprof
// endpoints so the profiler / blast-radius features can target monitor itself
// (and for debugging). Most useful with long-running modes (studio, watch,
// history record, mcp serve). It is a position-independent global flag so it
// works after any subcommand.
var pprofAddr string

func init() {
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
	// Bare `monitor` (no subcommand) prints help; the TUI is `monitor studio`.
	// cli.Version is set at link time by goreleaser's
	// -X .../internal/cli.Version=<tag> ldflag; "dev" for plain `go build`.
	cli.Execute()
}

// extractGlobalFlags scans the FULL argument tail for monitor's
// position-independent global flag --pprof, setting pprofAddr and returning the
// remaining args for cobra. Unlike flag.Parse it does not stop at the first
// non-flag token, so `monitor watch --pprof :6060` is honored rather than
// silently dropped. Supports --pprof=addr and --pprof addr; everything after a
// bare "--" is passed through verbatim so cobra's positional separator
// (e.g. `monitor vault -- cmd`) still works.
func extractGlobalFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		name, value, hasValue := splitFlag(a)
		if name == "pprof" {
			if hasValue {
				pprofAddr = value
			} else if i+1 < len(args) {
				pprofAddr = args[i+1]
				i++
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
