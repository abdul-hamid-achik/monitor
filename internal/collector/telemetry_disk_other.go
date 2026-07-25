//go:build !linux

package collector

import (
	"context"
	"fmt"
)

func telemetryDiskCounters(context.Context) (telemetryByteCounters, error) {
	return telemetryByteCounters{}, fmt.Errorf("anonymous aggregate disk counters are unsupported on this platform")
}
