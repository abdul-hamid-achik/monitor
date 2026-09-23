package procbind

import (
	"context"
	"testing"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func fakeListTCPListeners(conns []gnet.ConnectionStat, err error) func(context.Context) ([]gnet.ConnectionStat, error) {
	return func(context.Context) ([]gnet.ConnectionStat, error) { return conns, err }
}

func TestFindListenerPIDMatchesByPort(t *testing.T) {
	orig := listTCPListeners
	defer func() { listTCPListeners = orig }()
	listTCPListeners = fakeListTCPListeners([]gnet.ConnectionStat{
		{Status: "LISTEN", Laddr: gnet.Addr{IP: "127.0.0.1", Port: 9229}, Pid: 4242},
	}, nil)

	pid, ok := FindListenerPID(context.Background(), 9229)
	if !ok || pid != 4242 {
		t.Errorf("FindListenerPID = (%d, %v), want (4242, true)", pid, ok)
	}
}

func TestFindListenerPIDNoMatch(t *testing.T) {
	orig := listTCPListeners
	defer func() { listTCPListeners = orig }()
	listTCPListeners = fakeListTCPListeners([]gnet.ConnectionStat{
		{Status: "LISTEN", Laddr: gnet.Addr{IP: "127.0.0.1", Port: 9999}, Pid: 1},
	}, nil)

	if _, ok := FindListenerPID(context.Background(), 9229); ok {
		t.Error("FindListenerPID matched a port nothing is listening on")
	}
}

func TestFindListenerPIDIgnoresNonListenAndAnonymousOwner(t *testing.T) {
	orig := listTCPListeners
	defer func() { listTCPListeners = orig }()
	listTCPListeners = fakeListTCPListeners([]gnet.ConnectionStat{
		{Status: "ESTABLISHED", Laddr: gnet.Addr{IP: "127.0.0.1", Port: 9229}, Pid: 4242},
		{Status: "LISTEN", Laddr: gnet.Addr{IP: "127.0.0.1", Port: 9229}, Pid: 0}, // no visible owner
	}, nil)

	if _, ok := FindListenerPID(context.Background(), 9229); ok {
		t.Error("FindListenerPID must not trust a non-LISTEN row or an anonymous (pid<=0) owner")
	}
}

func TestFindListenerPIDZeroPort(t *testing.T) {
	if _, ok := FindListenerPID(context.Background(), 0); ok {
		t.Error("FindListenerPID(0) should never match")
	}
}
