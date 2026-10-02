package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

// session drives a Server over in-memory pipes the way a desktop client
// drives `monitor serve --stdio`.
type session struct {
	t       *testing.T
	in      *io.PipeWriter
	msgs    chan map[string]any
	done    chan error
	pending []map[string]any
}

func startSession(t *testing.T, svc *Service, opts Options) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{t: t, in: inW, msgs: make(chan map[string]any, 64), done: make(chan error, 1)}
	srv := New(svc, opts)
	go func() {
		err := srv.Run(context.Background(), inR, outW)
		_ = outW.Close()
		s.done <- err
	}()
	go func() {
		defer close(s.msgs)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("server wrote a non-JSON line %q: %v", sc.Text(), err)
				continue
			}
			s.msgs <- m
		}
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return s
}

func (s *session) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
}

// next returns the next message matching pred, keeping the others for a
// later call (responses can arrive out of order).
func (s *session) next(pred func(map[string]any) bool) map[string]any {
	s.t.Helper()
	for i, m := range s.pending {
		if pred(m) {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return m
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatal("server closed its output before the expected message")
			}
			if pred(m) {
				return m
			}
			s.pending = append(s.pending, m)
		case <-timeout:
			s.t.Fatalf("timed out waiting for a message; buffered: %v", s.pending)
		}
	}
}

func (s *session) response(id float64) map[string]any {
	return s.next(func(m map[string]any) bool { v, ok := m["id"].(float64); return ok && v == id })
}

func (s *session) notification(method string) map[string]any {
	return s.next(func(m map[string]any) bool { return m["method"] == method })
}

func errorCode(t *testing.T, m map[string]any) int {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error response, got %v", m)
	}
	return int(e["code"].(float64))
}

func TestHelloIsFirstAndListsOnlyWiredMethods(t *testing.T) {
	s := startSession(t, &Service{
		IssuesList: func(context.Context, IssuesListParams) (IssuesListResult, error) { return IssuesListResult{}, nil },
	}, Options{Version: "9.9.9"})
	first := <-s.msgs
	if first["method"] != "hello" {
		t.Fatalf("first message = %v, want the hello notification", first)
	}
	hello := first["params"].(map[string]any)
	if hello["protocol"] != Protocol || hello["monitor_version"] != "9.9.9" {
		t.Fatalf("hello = %v", hello)
	}
	methods := map[string]bool{}
	for _, m := range hello["methods"].([]any) {
		methods[m.(string)] = true
	}
	if !methods["issues.list"] || !methods["ping"] {
		t.Fatalf("methods %v missing issues.list or ping", methods)
	}
	if methods["issues.get"] || methods["process.kill"] {
		t.Fatalf("methods %v lists unwired services", methods)
	}
}

func TestErrorsFollowJSONRPC(t *testing.T) {
	s := startSession(t, &Service{}, Options{})
	s.notification("hello")

	s.send(`{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	if code := errorCode(t, s.response(1)); code != CodeMethodNotFound {
		t.Fatalf("unknown method code = %d", code)
	}

	s.send(`{not json`)
	parseErr := s.next(func(m map[string]any) bool { return m["id"] == nil && m["error"] != nil })
	if code := errorCode(t, parseErr); code != CodeParseError {
		t.Fatalf("parse error code = %d", code)
	}

	s.send(`{"jsonrpc":"1.0","id":2,"method":"ping"}`)
	if code := errorCode(t, s.response(2)); code != CodeInvalidRequest {
		t.Fatalf("wrong version code = %d", code)
	}

	s.send(``) // blank lines are ignored, not answered
	s.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if r := s.response(3); r["result"].(map[string]any)["pong"] != true {
		t.Fatalf("ping = %v", r)
	}
}

func TestReadOnlyRejectsMutations(t *testing.T) {
	called := false
	s := startSession(t, &Service{
		IssuesSetStatus: func(context.Context, SetStatusParams) (SetStatusResult, error) {
			called = true
			return SetStatusResult{}, nil
		},
	}, Options{ReadOnly: true})
	hello := s.notification("hello")["params"].(map[string]any)
	if hello["read_only"] != true {
		t.Fatalf("hello read_only = %v", hello["read_only"])
	}
	for _, m := range hello["methods"].([]any) {
		if m == "issues.set_status" {
			t.Fatal("a read-only hello must not advertise issues.set_status")
		}
	}
	s.send(`{"jsonrpc":"2.0","id":1,"method":"issues.set_status","params":{"ids":["ISS-1"],"status":"resolved"}}`)
	if code := errorCode(t, s.response(1)); code != CodeReadOnly {
		t.Fatalf("code = %d, want CodeReadOnly", code)
	}
	if called {
		t.Fatal("the service ran on a read-only server")
	}
}

func TestDestructiveMethodsRequireConfirm(t *testing.T) {
	var got KillParams
	s := startSession(t, &Service{
		Kill: func(_ context.Context, p KillParams) (any, error) {
			got = p
			return map[string]any{"killed": true}, nil
		},
	}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"process.kill","params":{"pid":4242}}`)
	if code := errorCode(t, s.response(1)); code != CodeConfirmRequired {
		t.Fatalf("code = %d, want CodeConfirmRequired", code)
	}
	if got.PID != 0 {
		t.Fatal("kill ran without confirm")
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"pid":4242,"confirm":true}}`)
	if r := s.response(2); r["error"] != nil {
		t.Fatalf("confirmed kill failed: %v", r)
	}
	if got.PID != 4242 || !got.Confirm {
		t.Fatalf("service got %+v", got)
	}
}

func TestServiceErrorsKeepTheirCode(t *testing.T) {
	s := startSession(t, &Service{
		IssueGet: func(context.Context, IssueGetParams) (IssueGetResult, error) {
			return IssueGetResult{}, NewError(CodeAmbiguous, "ambiguous", map[string]any{"candidates": []string{"ISS-A", "ISS-B"}})
		},
		IssueOccurrences: func(context.Context, OccurrencesParams) (OccurrencesResult, error) {
			return OccurrencesResult{}, errors.New("disk on fire")
		},
	}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"issues.get","params":{"id":"A"}}`)
	if code := errorCode(t, s.response(1)); code != CodeAmbiguous {
		t.Fatalf("code = %d", code)
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"issues.occurrences","params":{"id":"A"}}`)
	if code := errorCode(t, s.response(2)); code != CodeInternalError {
		t.Fatalf("code = %d", code)
	}
	s.send(`{"jsonrpc":"2.0","id":3,"method":"issues.get","params":{}}`)
	if code := errorCode(t, s.response(3)); code != CodeInvalidParams {
		t.Fatalf("missing id code = %d", code)
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	s := startSession(t, &Service{
		Doctor: func(context.Context) (any, error) { panic("boom") },
	}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"doctor"}`)
	if code := errorCode(t, s.response(1)); code != CodeInternalError {
		t.Fatalf("code = %d", code)
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	s.response(2) // the session survived
}

func TestSlowMethodDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	s := startSession(t, &Service{
		Doctor: func(ctx context.Context) (any, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return map[string]bool{"slow": true}, nil
		},
	}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"doctor"}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	s.response(2)
	close(release)
	s.response(1)
}

func TestListMarksTextUntrusted(t *testing.T) {
	s := startSession(t, &Service{
		IssuesList: func(context.Context, IssuesListParams) (IssuesListResult, error) {
			return IssuesListResult{Items: []issues.Issue{{ID: "ISS-1", Title: "boom"}}, Total: 1}, nil
		},
	}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"issues.list"}`)
	r := s.response(1)["result"].(map[string]any)
	if r["privacy"].(map[string]any)["text_is_untrusted"] != true {
		t.Fatalf("privacy = %v", r["privacy"])
	}
}

func TestEOFEndsTheSession(t *testing.T) {
	s := startSession(t, &Service{}, Options{})
	s.notification("hello")
	_ = s.in.Close()
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatalf("Run returned %v at EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin closed")
	}
}

func TestIssuesSubscriptionEmitsChangesAfterBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	digests := []IssueDigest{{ID: "ISS-OLD", Title: "old", OccurrenceCount: 1, Status: "open"}}
	baselineRead := make(chan struct{})
	var once sync.Once
	s := startSession(t, &Service{
		IssuesStorePath: func() (string, error) { return path, nil },
		IssueDigests: func(context.Context) ([]IssueDigest, error) {
			mu.Lock()
			defer mu.Unlock()
			defer once.Do(func() { close(baselineRead) })
			return append([]IssueDigest(nil), digests...), nil
		},
	}, Options{IssuesPoll: 10 * time.Millisecond})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"subscribe","params":{"topics":["issues"]}}`)
	s.response(1)
	select {
	case <-baselineRead:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscription never read its baseline")
	}

	mu.Lock()
	digests = append(digests, IssueDigest{ID: "ISS-NEW", Title: "new", OccurrenceCount: 1, Status: "open", LastSeen: time.Now()})
	mu.Unlock()
	if err := os.WriteFile(path, []byte("v2-longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := s.notification("issue.event")["params"].(map[string]any)
	if ev["type"] != "new" || ev["issue"].(map[string]any)["id"] != "ISS-NEW" {
		t.Fatalf("event = %v, want new ISS-NEW", ev)
	}
	for _, m := range s.pending {
		if m["method"] == "issue.event" {
			t.Fatalf("the baseline leaked an event: %v", m)
		}
	}
}

func TestUnknownTopicIsRejected(t *testing.T) {
	s := startSession(t, &Service{}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"subscribe","params":{"topics":["host"]}}`)
	if code := errorCode(t, s.response(1)); code != CodeInvalidParams {
		t.Fatalf("code = %d", code)
	}
}

func TestHostSubscriptionStreamsTicks(t *testing.T) {
	s := startSession(t, &Service{
		HostStream: func(ctx context.Context, p HostParams, interval time.Duration, emit func(HostTick)) error {
			if interval != minHostInterval {
				t.Errorf("interval = %v, want the %v floor", interval, minHostInterval)
			}
			emit(HostTick{PerCoreUsage: []float64{12.5}})
			<-ctx.Done()
			return nil
		},
	}, Options{})
	s.notification("hello")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"subscribe","params":{"topics":["host"],"host":{"interval_ms":10}}}`)
	s.response(1)
	tick := s.notification("host.tick")["params"].(map[string]any)
	if tick["per_core_usage"].([]any)[0].(float64) != 12.5 {
		t.Fatalf("tick = %v", tick)
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"unsubscribe","params":{"topics":["host"]}}`)
	r := s.response(2)["result"].(map[string]any)
	if len(r["subscribed"].([]any)) != 0 {
		t.Fatalf("still subscribed: %v", r)
	}
}

func TestDiffDigests(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	prev := map[string]IssueDigest{
		"A": {ID: "A", OccurrenceCount: 1, Status: "open", LastSeen: t0},
		"B": {ID: "B", OccurrenceCount: 5, Status: "resolved", LastSeen: t0},
		"C": {ID: "C", OccurrenceCount: 2, Status: "open", LastSeen: t0},
		"D": {ID: "D", OccurrenceCount: 2, Status: "open", LastSeen: t0},
	}
	next := map[string]IssueDigest{
		"A": {ID: "A", OccurrenceCount: 3, Status: "open", LastSeen: t0.Add(3 * time.Second)},
		"B": {ID: "B", OccurrenceCount: 6, ReopenedCount: 1, Status: "open", LastSeen: t0.Add(2 * time.Second)},
		"C": {ID: "C", OccurrenceCount: 2, Status: "ignored", LastSeen: t0},
		"D": {ID: "D", OccurrenceCount: 2, Status: "open", LastSeen: t0},
		"E": {ID: "E", OccurrenceCount: 1, Status: "open", LastSeen: t0.Add(4 * time.Second)},
	}
	events := diffDigests(prev, next)
	want := []struct {
		id, typ string
		delta   int64
	}{
		{"C", "status", 0},
		{"B", "regressed", 1},
		{"A", "occurrence", 2},
		{"E", "new", 1},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i, w := range want {
		if events[i].Issue.ID != w.id || events[i].Type != w.typ || events[i].Delta != w.delta {
			t.Fatalf("event %d = %s %s %d, want %s %s %d", i, events[i].Issue.ID, events[i].Type, events[i].Delta, w.id, w.typ, w.delta)
		}
		if !events[i].Privacy.TextIsUntrusted {
			t.Fatalf("event %d is not marked untrusted", i)
		}
	}
}
