//go:build linux

package collector

import (
	"context"
	"os"
)

func telemetryDiskCounters(ctx context.Context) (telemetryByteCounters, error) {
	if err := ctx.Err(); err != nil {
		return telemetryByteCounters{}, err
	}
	file, err := os.Open("/proc/vmstat")
	if err != nil {
		return telemetryByteCounters{}, err
	}
	defer file.Close()
	return parseAnonymousDiskCounters(file)
}
