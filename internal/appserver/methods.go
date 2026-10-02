package appserver

import (
	"context"
	"encoding/json"
	"strings"
)

// buildMethods is the method table. A method whose Service function is nil
// is left out entirely (CodeMethodNotFound, and absent from Hello).
func (s *Server) buildMethods() map[string]method {
	svc := s.svc
	m := map[string]method{
		"hello": {family: "session", call: func(context.Context, json.RawMessage) (any, error) {
			return s.Hello(), nil
		}},
		"ping": {family: "session", call: func(context.Context, json.RawMessage) (any, error) {
			return map[string]bool{"pong": true}, nil
		}},
		"subscribe":   {family: "session", call: s.subscribe},
		"unsubscribe": {family: "session", call: s.unsubscribe},
	}
	add := func(name string, ok bool, mt method) {
		if ok {
			m[name] = mt
		}
	}

	add("issues.list", svc.IssuesList != nil, method{family: "issues", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p IssuesListParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		res, err := svc.IssuesList(ctx, p)
		if err != nil {
			return nil, err
		}
		res.Privacy.TextIsUntrusted = true
		return res, nil
	}})
	add("issues.get", svc.IssueGet != nil, method{family: "issues", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p IssueGetParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ID) == "" {
			return nil, NewError(CodeInvalidParams, `issues.get: "id" is required (an id, short id/prefix, or "latest")`, nil)
		}
		return svc.IssueGet(ctx, p)
	}})
	add("issues.occurrences", svc.IssueOccurrences != nil, method{family: "issues", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p OccurrencesParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ID) == "" {
			return nil, NewError(CodeInvalidParams, `issues.occurrences: "id" is required`, nil)
		}
		res, err := svc.IssueOccurrences(ctx, p)
		if err != nil {
			return nil, err
		}
		res.Privacy.TextIsUntrusted = true
		return res, nil
	}})
	add("issues.histogram", svc.IssuesHistogram != nil, method{family: "issues", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p HistogramParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return svc.IssuesHistogram(ctx, p)
	}})
	add("projects.list", svc.Projects != nil, method{family: "issues", call: func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Projects(ctx)
	}})
	add("issues.set_status", svc.IssuesSetStatus != nil, method{family: "issues.write", mutating: true, call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p SetStatusParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if len(p.IDs) == 0 {
			return nil, NewError(CodeInvalidParams, `issues.set_status: "ids" is required`, nil)
		}
		switch p.Status {
		case "open", "resolved", "ignored":
		default:
			return nil, NewError(CodeInvalidParams, `issues.set_status: "status" must be open, resolved or ignored`, nil)
		}
		return svc.IssuesSetStatus(ctx, p)
	}})

	add("host.snapshot", svc.HostSnapshot != nil, method{family: "host", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p HostParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return svc.HostSnapshot(ctx, p)
	}})
	add("processes.list", svc.Processes != nil, method{family: "host", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p ProcessesParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return svc.Processes(ctx, p)
	}})
	add("process.kill", svc.Kill != nil, method{family: "process.kill", mutating: true, destructive: true, call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p KillParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if p.PID <= 0 {
			return nil, NewError(CodeInvalidParams, `process.kill: "pid" must be a positive pid`, nil)
		}
		return svc.Kill(ctx, p)
	}})

	add("profile.capture", svc.ProfileCapture != nil, method{family: "profile", mutating: true, destructive: true, call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p ProfileParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if p.PID <= 0 {
			return nil, NewError(CodeInvalidParams, `profile.capture: "pid" must be a positive pid`, nil)
		}
		return svc.ProfileCapture(ctx, p)
	}})
	add("heatmap.file", svc.HeatmapFile != nil, method{family: "heatmap", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p HeatmapFileParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Path) == "" {
			return nil, NewError(CodeInvalidParams, `heatmap.file: "path" is required`, nil)
		}
		return svc.HeatmapFile(ctx, p)
	}})

	add("launches.list", svc.Launches != nil, method{family: "launches", call: func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Launches(ctx)
	}})
	add("logs.search", svc.Logs != nil, method{family: "logs", call: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p LogsParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		res, err := svc.Logs(ctx, p)
		if err != nil {
			return nil, err
		}
		res.Privacy.TextIsUntrusted = true
		return res, nil
	}})
	add("incidents.list", svc.Incidents != nil, method{family: "incidents", call: func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Incidents(ctx)
	}})
	add("doctor", svc.Doctor != nil, method{family: "doctor", call: func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Doctor(ctx)
	}})
	return m
}
