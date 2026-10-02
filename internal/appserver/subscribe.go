package appserver

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"time"
)

// Topic names a client can subscribe to.
const (
	// TopicHost streams "host.tick" notifications (HostTick).
	TopicHost = "host"
	// TopicIssues streams "issue.event" notifications (IssueEvent).
	TopicIssues = "issues"
)

const (
	defaultHostInterval = time.Second
	minHostInterval     = 500 * time.Millisecond
	maxHostInterval     = time.Minute
	// issuesForceRefresh re-reads the digests every this many polls even
	// when the store file's stamp looks unchanged, in case a write landed
	// inside the filesystem's mtime resolution.
	issuesForceRefresh = 20
)

// subscribeParams is subscribe's input. Host configures the host topic.
type subscribeParams struct {
	Topics []string   `json:"topics"`
	Host   HostParams `json:"host,omitempty"`
}

// buildTopics lists the topics this server can stream.
func (s *Server) buildTopics() map[string]func(ctx context.Context, params json.RawMessage) {
	topics := map[string]func(ctx context.Context, params json.RawMessage){}
	if s.svc.HostStream != nil {
		topics[TopicHost] = s.runHost
	}
	if s.svc.IssueDigests != nil && s.svc.IssuesStorePath != nil {
		topics[TopicIssues] = func(ctx context.Context, _ json.RawMessage) { s.runIssues(ctx) }
	}
	return topics
}

// subscribe starts every requested topic. Subscribing to a topic that is
// already running restarts it with the new parameters.
func (s *Server) subscribe(ctx context.Context, raw json.RawMessage) (any, error) {
	var p subscribeParams
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Topics) == 0 {
		return nil, NewError(CodeInvalidParams, `subscribe: "topics" is required`, nil)
	}
	for _, t := range p.Topics {
		if _, ok := s.topics[t]; !ok {
			return nil, NewError(CodeInvalidParams, "subscribe: unknown topic "+t, map[string]any{"topics": s.Hello().Topics})
		}
	}
	hostParams, _ := json.Marshal(p.Host)
	for _, t := range p.Topics {
		s.startTopic(t, hostParams)
	}
	return map[string]any{"subscribed": s.activeTopics()}, nil
}

func (s *Server) unsubscribe(_ context.Context, raw json.RawMessage) (any, error) {
	var p subscribeParams
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	s.subsMu.Lock()
	for _, t := range p.Topics {
		if cancel, ok := s.subs[t]; ok {
			cancel()
			delete(s.subs, t)
		}
	}
	s.subsMu.Unlock()
	return map[string]any{"subscribed": s.activeTopics()}, nil
}

// startTopic runs topic in its own goroutine under a context that outlives
// the subscribe request (it ends at unsubscribe or at session end).
func (s *Server) startTopic(topic string, params json.RawMessage) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	if cancel, ok := s.subs[topic]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.subs[topic] = cancel
	run := s.topics[topic]
	s.subsWG.Add(1)
	go func() {
		defer s.subsWG.Done()
		run(ctx, params)
	}()
}

func (s *Server) activeTopics() []string {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	out := make([]string, 0, len(s.subs))
	for t := range s.subs {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (s *Server) stopAllSubscriptions() {
	s.subsMu.Lock()
	for t, cancel := range s.subs {
		cancel()
		delete(s.subs, t)
	}
	s.subsMu.Unlock()
	s.subsWG.Wait()
}

// runHost streams host.tick until ctx ends. A HostStream error ends the
// topic with one "topic.error" notification instead of a silent stop.
func (s *Server) runHost(ctx context.Context, raw json.RawMessage) {
	var p HostParams
	_ = json.Unmarshal(raw, &p)
	interval := time.Duration(p.IntervalMs) * time.Millisecond
	switch {
	case interval <= 0:
		interval = defaultHostInterval
	case interval < minHostInterval:
		interval = minHostInterval
	case interval > maxHostInterval:
		interval = maxHostInterval
	}
	err := s.svc.HostStream(ctx, p, interval, func(t HostTick) {
		s.notify("host.tick", t)
	})
	if err != nil && ctx.Err() == nil {
		s.notify("topic.error", map[string]string{"topic": TopicHost, "error": err.Error()})
	}
}

// fileStamp is what the issues topic compares between polls: veclite
// rewrites the whole file on every Save, so a write moves mtime or size.
type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

func statStamp(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size(), ok: true}
}

// runIssues polls the issue store and emits one issue.event per change.
// The first read is a silent baseline: a client lists issues itself and
// only wants what changes after it subscribed. A failed read (veclite
// v0.22.1's Save is not atomic, so a reader can land mid-write) is retried
// on the next poll without moving the baseline.
func (s *Server) runIssues(ctx context.Context) {
	path, err := s.svc.IssuesStorePath()
	if err != nil {
		s.notify("topic.error", map[string]string{"topic": TopicIssues, "error": err.Error()})
		return
	}
	ticker := time.NewTicker(s.opts.IssuesPoll)
	defer ticker.Stop()
	var (
		prev     map[string]IssueDigest
		last     fileStamp
		baseline bool
		polls    int
	)
	for {
		stamp := statStamp(path)
		if !baseline || stamp != last || polls%issuesForceRefresh == 0 {
			digests, err := s.svc.IssueDigests(ctx)
			if err == nil {
				next := make(map[string]IssueDigest, len(digests))
				for _, d := range digests {
					next[d.ID] = d
				}
				if baseline {
					for _, ev := range diffDigests(prev, next) {
						s.notify("issue.event", ev)
					}
				}
				prev, last, baseline = next, stamp, true
			}
		}
		polls++
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// diffDigests turns two digest sets into events, oldest activity first so
// a client applying them in order ends with the newest on top.
func diffDigests(prev, next map[string]IssueDigest) []IssueEvent {
	var events []IssueEvent
	for id, d := range next {
		p, seen := prev[id]
		ev := IssueEvent{Issue: d, Privacy: Privacy{TextIsUntrusted: true}}
		switch {
		case !seen:
			ev.Type = "new"
			ev.Delta = d.OccurrenceCount
		case d.ReopenedCount > p.ReopenedCount:
			ev.Type = "regressed"
			ev.Delta = d.OccurrenceCount - p.OccurrenceCount
		case d.OccurrenceCount > p.OccurrenceCount:
			ev.Type = "occurrence"
			ev.Delta = d.OccurrenceCount - p.OccurrenceCount
		case d.Status != p.Status:
			ev.Type = "status"
		default:
			continue
		}
		events = append(events, ev)
	}
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i].Issue, events[j].Issue
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.Before(b.LastSeen)
		}
		return a.ID < b.ID
	})
	return events
}
