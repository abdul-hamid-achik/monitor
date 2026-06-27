// Package profiler captures heap, CPU, goroutine, and macOS `sample` profiles
// for a process. For Go processes it scrapes net/http/pprof endpoints; for any
// other process it falls back to macOS `sample <pid>`.
package profiler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ProfileType is a discriminator for the kind of profile captured.
type ProfileType string

const (
	ProfileHeap      ProfileType = "heap"
	ProfileCPU       ProfileType = "cpu"
	ProfileGoroutine ProfileType = "goroutine"
	ProfileSample    ProfileType = "sample"
)

// Symbol is one parsed stack frame.
type Symbol struct {
	Func   string `json:"func"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Source string `json:"source,omitempty"`
	// Weight is the frame's share of the profile as a percentage (the pprof
	// "flat%"), when available (CPU profiles). 0 for formats without a
	// per-frame cost (e.g. the heap ?debug=1 dump).
	Weight float64 `json:"weight,omitempty"`
}

// Profile is the result of a capture.
type Profile struct {
	PID     int32       `json:"pid"`
	Type    ProfileType `json:"type"`
	Taken   time.Time   `json:"taken"`
	Path    string      `json:"path,omitempty"`
	Text    string      `json:"text,omitempty"`
	Symbols []Symbol    `json:"symbols,omitempty"`
}

// Capture takes a profile snapshot for the given pid. For the pprof types
// (heap/cpu/goroutine) it scrapes the net/http/pprof server at addr (default
// "localhost:6060" when addr is ""); the caller is responsible for pointing
// addr at the target pid's own pprof server — monitor cannot verify that the
// process answering on that port is actually `pid`. For the macOS sample
// type it runs `sample <pid>` and addr is ignored.
func Capture(ctx context.Context, pid int32, t ProfileType, addr string) (Profile, error) {
	p := Profile{PID: pid, Type: t, Taken: time.Now()}
	if pid <= 0 {
		return p, fmt.Errorf("invalid pid %d", pid)
	}
	switch t {
	case ProfileHeap, ProfileGoroutine:
		endpoint := pprofURL(addr, t)
		text, err := httpGet(ctx, endpoint)
		if err != nil {
			return p, fmt.Errorf("scrape %s: %w", endpoint, err)
		}
		p.Text = text
		p.Symbols = parsePprof(text)
		return p, nil
	case ProfileCPU:
		// The CPU endpoint returns gzipped protobuf, not text. Save it so
		// it's never lost and is analyzable with `go tool pprof`, then
		// best-effort symbolicate it via the go toolchain.
		endpoint := pprofURL(addr, t)
		body, err := httpGet(ctx, endpoint)
		if err != nil {
			return p, fmt.Errorf("scrape %s: %w", endpoint, err)
		}
		path, werr := writeTempProfile(pid, []byte(body))
		if werr != nil {
			return p, fmt.Errorf("save cpu profile: %w", werr)
		}
		p.Path = path
		p.Symbols = goToolPprofTop(ctx, path)
		return p, nil
	case ProfileSample:
		if runtime.GOOS != "darwin" {
			return p, fmt.Errorf("sample only available on macOS")
		}
		out, err := exec.CommandContext(ctx, "sample", fmt.Sprintf("%d", pid), "1", "-mayDie").CombinedOutput()
		if err != nil {
			return p, fmt.Errorf("sample: %w", err)
		}
		p.Text = string(out)
		p.Symbols = parseSample(string(out))
		return p, nil
	default:
		return p, fmt.Errorf("unknown profile type %q", t)
	}
}

// defaultPprofAddr is the host:port scraped when Capture is given no address.
const defaultPprofAddr = "localhost:6060"

// pprofURL maps a ProfileType to its net/http/pprof endpoint at addr (host:port,
// defaulting to localhost:6060 when empty).
//   - CPU is served at /debug/pprof/profile (there is no /cpu handler),
//     bounded with ?seconds=1 so the scrape can't block indefinitely. It
//     returns protobuf, so parsePprof extracts no symbols from it.
//   - heap/goroutine use ?debug=1 to get a TEXT dump (addr / func+off /
//     file.go:line frames) that parsePprof can read; without it the body
//     is gzipped protobuf and no symbols are recoverable.
func pprofURL(addr string, t ProfileType) string {
	if addr == "" {
		addr = defaultPprofAddr
	}
	base := "http://" + addr + "/debug/pprof/"
	switch t {
	case ProfileCPU:
		return base + "profile?seconds=1"
	case ProfileHeap, ProfileGoroutine:
		return base + string(t) + "?debug=1"
	default:
		return base + string(t)
	}
}

// pprofClient bounds a scrape so a hung/slow pprof endpoint can't stall
// forever when the caller passes a context without a deadline. The timeout
// comfortably covers the bounded CPU profile (?seconds=1).
var pprofClient = &http.Client{Timeout: 30 * time.Second}

// writeTempProfile saves raw profile bytes (a CPU protobuf) to a temp file
// and returns its path so the profile is preserved for `go tool pprof`.
func writeTempProfile(pid int32, body []byte) (string, error) {
	f, err := os.CreateTemp("", fmt.Sprintf("monitor-cpu-%d-*.pb.gz", pid))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// goToolPprofTop best-effort symbolicates a saved profile via the go
// toolchain (`go tool pprof -top -lines`). Returns nil when `go` isn't on
// PATH or the command fails, so the caller still gets the saved profile path.
func goToolPprofTop(ctx context.Context, path string) []Symbol {
	if _, err := exec.LookPath("go"); err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "go", "tool", "pprof",
		"-top", "-lines", "-nodecount=25", path).Output()
	if err != nil {
		return nil
	}
	return parsePprofTop(string(out))
}

// parsePprofTop parses `go tool pprof -top -lines` output. Each data row is
//
//	<flat> <flat%> <sum%> <cum> <cum%>  <func>  <file>:<line>
//
// — the func and file:line are the last two whitespace fields, and the
// leading column is a sample magnitude (starts with a digit), which lets us
// skip the header/prose lines.
func parsePprofTop(text string) []Symbol {
	sc := bufio.NewScanner(strings.NewReader(text))
	var syms []Symbol
	seen := map[string]bool{}
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) < 7 || parts[0] == "" || parts[0][0] < '0' || parts[0][0] > '9' {
			continue
		}
		fileLine := parts[len(parts)-1]
		fn := parts[len(parts)-2]
		colon := strings.LastIndexByte(fileLine, ':')
		if colon < 0 {
			continue
		}
		var ln int
		if _, err := fmt.Sscanf(fileLine[colon+1:], "%d", &ln); err != nil {
			continue
		}
		if seen[fn] {
			continue
		}
		seen[fn] = true
		// flat% is the 2nd column, e.g. "87.96%".
		var weight float64
		fmt.Sscanf(strings.TrimSuffix(parts[1], "%"), "%g", &weight)
		syms = append(syms, Symbol{Func: fn, File: fileLine[:colon], Line: ln, Weight: weight})
		if len(syms) >= 25 {
			break
		}
	}
	return syms
}

func httpGet(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := pprofClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// parsePprof extracts the top frames from a pprof text dump.
func parsePprof(text string) []Symbol {
	sc := bufio.NewScanner(strings.NewReader(text))
	var syms []Symbol
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "File:") || strings.HasPrefix(line, "Build") || strings.HasPrefix(line, "Type:") {
			continue
		}
		// lines like `     100ms  40%  main.runLoop  foo.go:42  0x1234`
		parts := strings.Fields(line)
		// walk from the end; func name is the field before file.go:line
		for i := len(parts) - 1; i >= 0; i-- {
			p := parts[i]
			colon := strings.LastIndexByte(p, ':')
			if colon < 0 || !strings.Contains(p, ".go:") {
				continue
			}
			path := p[:colon]
			var lineNum int
			if _, err := fmt.Sscanf(p[colon+1:], "%d", &lineNum); err != nil {
				break
			}
			if i == 0 {
				break
			}
			fn := parts[i-1]
			// strip a trailing program-counter offset like "+0x9b" that the
			// pprof ?debug=1 text format appends to the function name.
			if plus := strings.LastIndexByte(fn, '+'); plus > 0 && strings.HasPrefix(fn[plus+1:], "0x") {
				fn = fn[:plus]
			}
			// skip percentage tokens like "40%" or "100ms"
			if strings.HasSuffix(fn, "%") || strings.HasSuffix(fn, "ms") || strings.HasSuffix(fn, "s") {
				fn = "unknown"
			}
			if seen[fn+path] {
				break
			}
			seen[fn+path] = true
			syms = append(syms, Symbol{Func: fn, File: path, Line: lineNum})
			if len(syms) >= 25 {
				return syms
			}
			break
		}
	}
	return syms
}

// sampleFrameRe matches a macOS `sample` call-graph frame line, e.g.
//
//	"          869 nanosleep  (in libsystem_c.dylib) + 220  [0x180705cc0]"
//
// Group 1 is the function name, group 2 the containing image. The required
// leading sample count anchors it so the Thread header line (ends in
// "(serial)", no "(in …)"), the "Sort by top of stack" rows (no leading
// count), and the Binary Images table (different shape) are all excluded.
var sampleFrameRe = regexp.MustCompile(`^\s*\d+\s+(.+?)\s+\(in\s+([^)]+)\)`)

// parseSample extracts the unique stack frames from macOS `sample` output.
func parseSample(text string) []Symbol {
	sc := bufio.NewScanner(strings.NewReader(text))
	var syms []Symbol
	seen := map[string]bool{}
	for sc.Scan() {
		m := sampleFrameRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		fn := strings.TrimSpace(m[1])
		if fn == "" || seen[fn] {
			continue
		}
		seen[fn] = true
		// File carries the containing image (sample has no file:line info).
		syms = append(syms, Symbol{Func: fn, File: strings.TrimSpace(m[2])})
		if len(syms) >= 25 {
			break
		}
	}
	return syms
}

// ToJSON is a convenience for CLI --json output.
func (p Profile) ToJSON() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }