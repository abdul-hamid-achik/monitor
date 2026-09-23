package stacktrace

// Level constants for Exception.Level.
const (
	LevelFatal   = "fatal"
	LevelError   = "error"
	LevelWarning = "warning"
)

// levelFromLogPrefix maps a logging-module-style level token (as seen in
// Python's "ERROR:root:msg" or a zap console line's level column) to one of
// the three Exception.Level values. It returns ("", false) for a token it
// doesn't recognize, so callers can fall back to their own default instead
// of silently guessing.
func levelFromLogPrefix(tok string) (string, bool) {
	switch tok {
	case "CRITICAL", "FATAL", "fatal", "dpanic", "DPANIC", "panic", "PANIC":
		return LevelFatal, true
	case "ERROR", "error", "ERR":
		return LevelError, true
	case "WARNING", "WARN", "warning", "warn":
		return LevelWarning, true
	default:
		return "", false
	}
}
