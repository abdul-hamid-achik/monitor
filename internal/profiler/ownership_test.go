package profiler

import (
	"context"
	"errors"
	"strings"
	"testing"

	gnet "github.com/shirou/gopsutil/v4/net"
)

// listenConn builds a minimal LISTEN entry for the given port/pid.
func listenConn(status string, port uint32, pid int32) gnet.ConnectionStat {
	return gnet.ConnectionStat{
		Status: status,
		Laddr:  gnet.Addr{IP: "127.0.0.1", Port: port},
		Pid:    pid,
	}
}

func TestVerifyListenerOwnership(t *testing.T) {
	origList := listTCPConnections
	defer func() { listTCPConnections = origList }()

	tests := []struct {
		name           string
		addr           string
		conns          []gnet.ConnectionStat
		stubErr        error
		want           PortOwnership
		wantDetail     string
		wantDetailNone bool
	}{
		{
			name:           "owned",
			addr:           "localhost:6060",
			conns:          []gnet.ConnectionStat{listenConn("LISTEN", 6060, 42)},
			want:           OwnershipOwned,
			wantDetailNone: true,
		},
		{
			name: "dual_stack_second_entry_owned",
			addr: "localhost:6060",
			conns: []gnet.ConnectionStat{
				listenConn("LISTEN", 6060, 7),
				listenConn("LISTEN", 6060, 42),
			},
			want: OwnershipOwned,
		},
		{
			name:       "other_owner",
			addr:       "localhost:6060",
			conns:      []gnet.ConnectionStat{listenConn("LISTEN", 6060, 7)},
			want:       OwnershipNotOwned,
			wantDetail: "pid 7",
		},
		{
			name:  "anonymous_listener",
			addr:  "localhost:6060",
			conns: []gnet.ConnectionStat{listenConn("LISTEN", 6060, 0)},
			want:  OwnershipUnknown,
		},
		{
			name:       "nothing_listening",
			addr:       "localhost:7070",
			conns:      []gnet.ConnectionStat{listenConn("LISTEN", 6060, 42)},
			want:       OwnershipNotOwned,
			wantDetail: "nothing is listening",
		},
		{
			name:  "non_listen_ignored",
			addr:  "localhost:6060",
			conns: []gnet.ConnectionStat{listenConn("ESTABLISHED", 6060, 42)},
			want:  OwnershipNotOwned,
		},
		{
			name:    "enumeration_error",
			addr:    "localhost:6060",
			stubErr: errors.New("lsof failed"),
			want:    OwnershipUnknown,
		},
		{
			name: "bad_addr",
			addr: "not-an-addr",
			want: OwnershipUnknown,
		},
		{
			name:  "empty_addr_defaults_6060",
			addr:  "",
			conns: []gnet.ConnectionStat{listenConn("LISTEN", 6060, 42)},
			want:  OwnershipOwned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listTCPConnections = func(context.Context) ([]gnet.ConnectionStat, error) {
				if tt.stubErr != nil {
					return nil, tt.stubErr
				}
				return tt.conns, nil
			}
			got, detail := VerifyListenerOwnership(context.Background(), 42, tt.addr)
			if got != tt.want {
				t.Errorf("VerifyListenerOwnership() = %v, detail=%q, want %v", got, detail, tt.want)
			}
			if tt.wantDetailNone && detail != "" {
				t.Errorf("expected empty detail for owned; got %q", detail)
			}
			if tt.wantDetail != "" && !strings.Contains(detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", detail, tt.wantDetail)
			}
		})
	}
}
