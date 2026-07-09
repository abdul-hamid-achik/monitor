package profiler

import (
	"context"
	"fmt"
	"net"
	"strconv"

	gnet "github.com/shirou/gopsutil/v4/net"
)

// PortOwnership classifies whether the TCP listener at a pprof address is
// proven to belong to a specific PID. Only OwnershipOwned is positive proof;
// everything else means "do not trust the endpoint for this pid".
type PortOwnership string

const (
	OwnershipOwned    PortOwnership = "owned"
	OwnershipNotOwned PortOwnership = "not_owned"
	OwnershipUnknown  PortOwnership = "unknown"
)

// listTCPConnections is a stub point for tests. On macOS gopsutil shells out
// to lsof, which without root may not see other users' sockets — that case
// surfaces as a listener with Pid<=0 (or no listener at all) and maps to
// unknown/not_owned below, never to owned.
var listTCPConnections = func(ctx context.Context) ([]gnet.ConnectionStat, error) {
	return gnet.ConnectionsWithContext(ctx, "tcp")
}

// VerifyListenerOwnership reports whether the LISTEN socket at addr
// (host:port; "" means DefaultPprofAddr) provably belongs to pid.
// The returned detail is empty for owned and human-readable otherwise.
func VerifyListenerOwnership(ctx context.Context, pid int32, addr string) (PortOwnership, string) {
	if addr == "" {
		addr = DefaultPprofAddr
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return OwnershipUnknown, fmt.Sprintf("cannot parse pprof addr %q: %v", addr, err)
	}
	port64, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return OwnershipUnknown, fmt.Sprintf("cannot parse pprof port %q: %v", portStr, err)
	}
	port := uint32(port64)
	conns, err := listTCPConnections(ctx)
	if err != nil {
		return OwnershipUnknown, fmt.Sprintf("cannot enumerate tcp listeners: %v", err)
	}
	var otherOwner int32 = -1
	sawAnonymous := false
	for _, c := range conns {
		if c.Status != "LISTEN" || c.Laddr.Port != port {
			continue
		}
		if c.Pid == pid { // any matching entry (v4/v6 dual-stack) is proof
			return OwnershipOwned, ""
		}
		if c.Pid > 0 {
			otherOwner = c.Pid
		} else {
			sawAnonymous = true
		}
	}
	switch {
	case otherOwner > 0:
		return OwnershipNotOwned, fmt.Sprintf("port %d is owned by pid %d, not pid %d", port, otherOwner, pid)
	case sawAnonymous:
		return OwnershipUnknown, fmt.Sprintf("listener on port %d has no visible owner (insufficient permissions to inspect)", port)
	default:
		return OwnershipNotOwned, fmt.Sprintf("nothing is listening on port %d", port)
	}
}
