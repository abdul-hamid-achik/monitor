//go:build linux

package collector

import (
	"context"
	"errors"
	"os"
)

func telemetryNetworkCounters(ctx context.Context) (telemetryByteCounters, error) {
	if err := ctx.Err(); err != nil {
		return telemetryByteCounters{}, err
	}
	ipv4File, err := os.Open("/proc/net/netstat")
	if err != nil {
		return telemetryByteCounters{}, err
	}
	ipv4, parseErr := parseAnonymousIPv4Counters(ipv4File)
	closeErr := ipv4File.Close()
	if parseErr != nil {
		return telemetryByteCounters{}, parseErr
	}
	if closeErr != nil {
		return telemetryByteCounters{}, closeErr
	}

	if err := ctx.Err(); err != nil {
		return telemetryByteCounters{}, err
	}
	ipv6File, err := os.Open("/proc/net/snmp6")
	if errors.Is(err, os.ErrNotExist) {
		return ipv4, nil
	}
	if err != nil {
		return telemetryByteCounters{}, err
	}
	ipv6, parseErr := parseAnonymousIPv6Counters(ipv6File)
	closeErr = ipv6File.Close()
	if parseErr != nil {
		return telemetryByteCounters{}, parseErr
	}
	if closeErr != nil {
		return telemetryByteCounters{}, closeErr
	}
	return addAnonymousByteCounters(ipv4, ipv6)
}
