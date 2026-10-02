// Package appserver implements monitor.app.v1: the JSON-RPC 2.0 protocol
// `monitor serve --stdio` speaks to a desktop client (Monitor Desktop),
// one message per line on stdin/stdout.
//
// The same process serves a local client (spawned directly) and a remote
// one (spawned as `ssh host monitor serve --stdio`): stdio is the only
// transport, so there is no port, no token and no listener to protect, and
// the server's lifetime is its client's — EOF on stdin ends it.
//
// The server is never the only write path. Every mutation goes through the
// same short-lived writers the CLI uses (issues.WithWriter), and every
// read opens the stores read-only, so the CLI keeps working beside it and
// without it. --read-only (Options.ReadOnly) rejects every mutating method,
// for doors that must stay read-only (Chalupa's `task monitor`).
//
// Like internal/mcp, handlers only decode params and copy fields: the
// rules (scrubbing, protected-process refusal, store access) live in the
// Service implementation in internal/cli/serve.go.
package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"
)

// maxRequestBytes bounds one inbound line. Requests are small (ids and
// filters); anything near this is a client bug, not a query.
const maxRequestBytes = 1 << 20

// defaultMaxConcurrent bounds in-flight requests. Slow methods (a 5 s CPU
// profile, doctor's tool probes) must not starve quick ones, but a client
// cannot fan out unbounded work either: past this, reading stalls until a
// slot frees up.
const defaultMaxConcurrent = 8

// Options configures a Server.
type Options struct {
	// Version is monitor's own version, announced in Hello.
	Version string
	// ReadOnly rejects every mutating method with CodeReadOnly.
	ReadOnly bool
	// IssuesPoll is how often the issues subscription stats the store
	// file. Zero means 500 ms.
	IssuesPoll time.Duration
	// MaxConcurrent bounds in-flight requests. Zero means 8.
	MaxConcurrent int
}

// handlerFunc runs one method with its raw params.
type handlerFunc func(ctx context.Context, params json.RawMessage) (any, error)

type method struct {
	// family is the Hello capability this method belongs to.
	family string
	// mutating methods are rejected on a read-only server.
	mutating bool
	// destructive methods also require "confirm": true in their params.
	destructive bool
	call        handlerFunc
}

// Server is one monitor.app.v1 session.
type Server struct {
	svc     *Service
	opts    Options
	methods map[string]method
	topics  map[string]func(ctx context.Context, params json.RawMessage)

	writeMu sync.Mutex
	out     *bufio.Writer
	outErr  error

	subsMu sync.Mutex
	subs   map[string]context.CancelFunc
	subsWG sync.WaitGroup
}

// New builds a Server over svc.
func New(svc *Service, opts Options) *Server {
	if svc == nil {
		svc = &Service{}
	}
	if opts.IssuesPoll <= 0 {
		opts.IssuesPoll = 500 * time.Millisecond
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultMaxConcurrent
	}
	s := &Server{svc: svc, opts: opts, subs: map[string]context.CancelFunc{}}
	s.methods = s.buildMethods()
	s.topics = s.buildTopics()
	return s
}

// Hello returns the announcement this server writes first.
func (s *Server) Hello() Hello {
	host, _ := os.Hostname()
	families := map[string]bool{}
	methods := make([]string, 0, len(s.methods))
	for name, m := range s.methods {
		if s.opts.ReadOnly && m.mutating {
			continue
		}
		methods = append(methods, name)
		families[m.family] = true
	}
	sort.Strings(methods)
	capabilities := make([]string, 0, len(families))
	for f := range families {
		capabilities = append(capabilities, f)
	}
	sort.Strings(capabilities)
	topics := make([]string, 0, len(s.topics))
	for t := range s.topics {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	return Hello{
		Protocol:       Protocol,
		MonitorVersion: s.opts.Version,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Hostname:       host,
		ReadOnly:       s.opts.ReadOnly,
		Capabilities:   capabilities,
		Methods:        methods,
		Topics:         topics,
	}
}

// Run serves one session: it writes the hello notification, then answers
// every request read from in until in reaches EOF or ctx ends. Requests run
// concurrently (bounded by Options.MaxConcurrent), so responses may arrive
// out of order; a client matches them by id. Run returns after every
// in-flight request and subscription has stopped.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.out = bufio.NewWriter(out)
	s.notify("hello", s.Hello())

	sem := make(chan struct{}, s.opts.MaxConcurrent)
	var inflight sync.WaitGroup
	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), maxRequestBytes)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				scanErr <- nil
				return
			}
		}
		scanErr <- scanner.Err()
	}()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case line, ok := <-lines:
			if !ok {
				break loop
			}
			req, perr := parseRequest(line)
			if perr != nil {
				if perr.Code != 0 {
					s.reply(req.ID, nil, perr)
				}
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break loop
			}
			inflight.Add(1)
			go func() {
				defer inflight.Done()
				defer func() { <-sem }()
				s.handle(ctx, req)
			}()
		}
	}
	// EOF is a client saying "no more requests", not "abandon the ones in
	// flight": they finish under a live context and their answers are
	// written (a 5 s profile capture piped in by a script must not be cut
	// short). A signal cancels ctx itself, which does stop them.
	inflight.Wait()
	cancel()
	s.stopAllSubscriptions()
	var err error
	select {
	case err = <-scanErr:
	default:
	}
	if err != nil {
		return fmt.Errorf("read requests: %w", err)
	}
	return nil
}

// parseRequest decodes one line. A blank line is skipped (zero-code error);
// anything that is not a JSON-RPC 2.0 request gets an error response.
func parseRequest(line []byte) (request, *Error) {
	trimmed := bytesTrimSpace(line)
	if len(trimmed) == 0 {
		return request{}, &Error{}
	}
	var req request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return request{ID: json.RawMessage("null")}, NewError(CodeParseError, "parse error: "+err.Error(), nil)
	}
	if req.JSONRPC != jsonrpcVersion || req.Method == "" {
		if req.isNotification() {
			req.ID = json.RawMessage("null")
		}
		return req, NewError(CodeInvalidRequest, `invalid request: want {"jsonrpc":"2.0","method":...}`, nil)
	}
	return req, nil
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}

// handle runs one request and writes its response (none for a
// notification).
func (s *Server) handle(ctx context.Context, req request) {
	result, err := s.dispatch(ctx, req)
	if req.isNotification() {
		return
	}
	if err != nil {
		var rpcErr *Error
		if !errors.As(err, &rpcErr) {
			rpcErr = NewError(CodeInternalError, err.Error(), nil)
		}
		s.reply(req.ID, nil, rpcErr)
		return
	}
	if result == nil {
		result = struct{}{}
	}
	s.reply(req.ID, result, nil)
}

func (s *Server) dispatch(ctx context.Context, req request) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, NewError(CodeInternalError, fmt.Sprintf("internal error in %s: %v", req.Method, r), nil)
		}
	}()
	m, ok := s.methods[req.Method]
	if !ok {
		return nil, NewError(CodeMethodNotFound, "method not found: "+req.Method, nil)
	}
	if m.mutating && s.opts.ReadOnly {
		return nil, NewError(CodeReadOnly, req.Method+" is not allowed: this server is --read-only", nil)
	}
	if m.destructive {
		var c struct {
			Confirm bool `json:"confirm"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &c)
		}
		if !c.Confirm {
			return nil, NewError(CodeConfirmRequired, req.Method+` changes the host: retry with "confirm": true after the user confirms`, nil)
		}
	}
	return m.call(ctx, req.Params)
}

// decode unmarshals params into v; absent params leave v at its zero value.
func decode(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return NewError(CodeInvalidParams, "invalid params: "+err.Error(), nil)
	}
	return nil
}

func (s *Server) reply(id json.RawMessage, result any, rpcErr *Error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.write(response{JSONRPC: jsonrpcVersion, ID: id, Result: result, Error: rpcErr})
}

func (s *Server) notify(name string, params any) {
	s.write(notification{JSONRPC: jsonrpcVersion, Method: name, Params: params})
}

// write encodes v as one line. A failed write (the client went away) is
// remembered and every later write is dropped; Run still ends at EOF.
func (s *Server) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(response{
			JSONRPC: jsonrpcVersion, ID: json.RawMessage("null"),
			Error: NewError(CodeInternalError, "encode response: "+err.Error(), nil),
		})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.outErr != nil {
		return
	}
	if _, err := s.out.Write(append(data, '\n')); err != nil {
		s.outErr = err
		return
	}
	s.outErr = s.out.Flush()
}
