package procbind

import (
	"context"

	gnet "github.com/shirou/gopsutil/v4/net"
)

// listTCPListeners is a stub point for tests; production code always uses
// gopsutil's real connection table. Kept as a package var (rather than a
// parameter threaded through every caller) for the same reason
// internal/profiler/ownership.go's listTCPConnections is: this is a leaf,
// rarely-mocked dependency, not part of any exported API surface.
var listTCPListeners = func(ctx context.Context) ([]gnet.ConnectionStat, error) {
	return gnet.ConnectionsWithContext(ctx, "tcp")
}

// FindListenerPID reports which live process owns the TCP LISTEN socket at
// port, on any local address (127.0.0.1, ::1, 0.0.0.0, ...) -- E3.3b's
// "map every inspector banner to its pid via the listening-port owner"
// rule (the naming ADR §8). An inspector's startup
// banner ("Debugger listening on ws://host:port/uuid") names a port but no
// pid at all, so this is the ONLY source of truth for which process it
// belongs to -- the same "prove it by the socket's owner, never by
// self-reported text" posture internal/profiler/ownership.go's
// VerifyListenerOwnership already applies in the other direction (a known
// pid, an unknown port owner). Returns ok=false when nothing is currently
// listening on port (already exited, or the banner raced this lookup) or
// gopsutil could not attribute an owner (insufficient permissions to
// inspect another user's socket) -- both cases mean "do not trust this
// port", never a fabricated pid.
//
// A port with more than one LISTEN entry (IPv4 and IPv6 dual-stack binds
// of the SAME process, the common case for "127.0.0.1:PORT" style
// addresses) is not a conflict: every matching entry names the same real
// pid in practice, and the first one found is returned.
func FindListenerPID(ctx context.Context, port int) (int32, bool) {
	if port <= 0 {
		return 0, false
	}
	conns, err := listTCPListeners(ctx)
	if err != nil {
		return 0, false
	}
	for _, c := range conns {
		if c.Status != "LISTEN" || int(c.Laddr.Port) != port {
			continue
		}
		if c.Pid > 0 {
			return c.Pid, true
		}
	}
	return 0, false
}
