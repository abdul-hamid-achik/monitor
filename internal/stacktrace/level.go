package stacktrace

import "strings"

// Level constants for Exception.Level.
const (
	LevelFatal   = "fatal"
	LevelError   = "error"
	LevelWarning = "warning"
)

// levelFromLogPrefix maps a logger's level token (Python's "ERROR:root:msg",
// a zap level column in any case, a Ruby Logger severity, a tslog level) to
// one of the three Exception.Level values. It returns ("", false) for a token
// that is not warning-or-worse (debug/info/trace) or not a level at all, so
// callers never turn ordinary chatter into an event.
func levelFromLogPrefix(tok string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(tok)) {
	case "CRITICAL", "FATAL", "DPANIC", "PANIC", "ANY", "UNKNOWN":
		return LevelFatal, true
	case "ERROR", "ERR":
		return LevelError, true
	case "WARNING", "WARN":
		return LevelWarning, true
	default:
		return "", false
	}
}
