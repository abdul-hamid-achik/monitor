package appserver

import "encoding/json"

// Protocol is the contract name the server announces in its hello
// notification. A client checks it (and Capabilities) before calling any
// method; see docs/contracts/app-protocol-v1.md.
const Protocol = "monitor.app.v1"

// jsonrpcVersion is the only JSON-RPC version the server speaks.
const jsonrpcVersion = "2.0"

// Error codes. The -327xx range is JSON-RPC 2.0's own; the -320xx range is
// this protocol's, one code per outcome a client handles differently.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// CodeConfirmRequired: a destructive method was called without
	// confirm:true. The client shows its own confirmation and retries.
	CodeConfirmRequired = -32001
	// CodeReadOnly: a mutating method was called on a --read-only server.
	CodeReadOnly = -32002
	// CodeNotFound: the id (issue, process, file) does not exist.
	CodeNotFound = -32003
	// CodeUnavailable: the capability exists but cannot work for this
	// target (Bun's JSC inspector, a missing tool). data carries
	// limitation/recovery.
	CodeUnavailable = -32004
	// CodeAmbiguous: a short id/prefix matched more than one issue. data
	// carries the candidates.
	CodeAmbiguous = -32005
	// CodeRefused: the target is protected (a system process for
	// process.kill). Never overridable by confirm.
	CodeRefused = -32006
)

// request is one inbound JSON-RPC message. ID is kept raw so it is echoed
// back byte-for-byte (number or string); a missing ID marks a notification,
// which never gets a response.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r request) isNotification() bool { return len(r.ID) == 0 }

// response is one outbound reply. Exactly one of Result and Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// notification is one outbound server-initiated message (hello, host.tick,
// alert, issue.event).
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Error is a JSON-RPC error object. Service implementations return it (via
// NewError and friends) when an outcome has its own code; any other error
// becomes CodeInternalError with the error's text.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// NewError builds an *Error with an optional data payload.
func NewError(code int, message string, data any) *Error {
	return &Error{Code: code, Message: message, Data: data}
}

// Hello is the first message the server writes: a notification named
// "hello", also returned by the "hello" method for clients that prefer a
// request. Capabilities lists the method families this server can answer;
// a family missing from the list means its Service function was not wired.
type Hello struct {
	Protocol       string   `json:"protocol"`
	MonitorVersion string   `json:"monitor_version"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	Hostname       string   `json:"hostname,omitempty"`
	ReadOnly       bool     `json:"read_only"`
	Capabilities   []string `json:"capabilities"`
	Methods        []string `json:"methods"`
	Topics         []string `json:"topics"`
}
