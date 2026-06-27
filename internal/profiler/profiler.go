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
	"os/exec"
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

// Capture takes a profile snapshot for the given pid.
func Capture(ctx context.Context, pid int32, t ProfileType) (Profile, error) {
	p := Profile{PID: pid, Type: t, Taken: time.Now()}
	if pid <= 0 {
		return p, fmt.Errorf("invalid pid %d", pid)
	}
	switch t {
	case ProfileHeap, ProfileCPU, ProfileGoroutine:
		endpoint := fmt.Sprintf("http://localhost:6060/debug/pprof/%s", t)
		text, err := httpGet(ctx, endpoint)
		if err != nil {
			return p, fmt.Errorf("scrape %s: %w", endpoint, err)
		}
		p.Text = text
		p.Symbols = parsePprof(text)
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

func httpGet(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
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

// parseSample extracts frames from `sample` output.
func parseSample(text string) []Symbol {
	sc := bufio.NewScanner(strings.NewReader(text))
	var syms []Symbol
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "0x") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		fn := parts[len(parts)-1]
		if seen[fn] {
			continue
		}
		seen[fn] = true
		syms = append(syms, Symbol{Func: fn})
		if len(syms) >= 25 {
			break
		}
	}
	return syms
}

// ToJSON is a convenience for CLI --json output.
func (p Profile) ToJSON() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }