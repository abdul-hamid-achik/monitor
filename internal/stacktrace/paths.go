package stacktrace

import "strings"

func isDriveLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// isWindowsAbs reports a drive-letter ("C:\x", "C:/x") or UNC ("\\srv\x")
// absolute path.
func isWindowsAbs(p string) bool {
	if len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return true
	}
	return strings.HasPrefix(p, `\\`)
}

// isAbsPath reports whether p is an absolute filesystem path, POSIX or
// Windows, regardless of the OS monitor runs on.
func isAbsPath(p string) bool { return strings.HasPrefix(p, "/") || isWindowsAbs(p) }
