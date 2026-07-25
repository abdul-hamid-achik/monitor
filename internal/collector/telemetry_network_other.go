//go:build !linux

package collector

import (
	"context"
	"fmt"
)

func telemetryNetworkCounters(context.Context) (telemetryByteCounters, error) {
	return telemetryByteCounters{}, fmt.Errorf("anonymous aggregate network counters are unsupported on this platform")
}
