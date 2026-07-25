package collector

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const procDiskCounterBytes = 1024

// parseAnonymousDiskCounters reads only Linux's aggregate paging counters.
// The input contains metric names but no device, serial, label, mount, or path
// identities. pgpgin/pgpgout are KiB counters, converted here to bytes.
func parseAnonymousDiskCounters(r io.Reader) (telemetryByteCounters, error) {
	var (
		counters        telemetryByteCounters
		readObserved    bool
		writtenObserved bool
	)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 2 || (parts[0] != "pgpgin" && parts[0] != "pgpgout") {
			continue
		}
		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return telemetryByteCounters{}, fmt.Errorf("parse aggregate disk counter %s: %w", parts[0], err)
		}
		if value > math.MaxUint64/procDiskCounterBytes {
			return telemetryByteCounters{}, fmt.Errorf("aggregate disk counter %s overflows bytes", parts[0])
		}
		value *= procDiskCounterBytes
		if parts[0] == "pgpgin" {
			counters.readBytes = value
			readObserved = true
		} else {
			counters.writeBytes = value
			writtenObserved = true
		}
	}
	if err := scanner.Err(); err != nil {
		return telemetryByteCounters{}, fmt.Errorf("read aggregate disk counters: %w", err)
	}
	if !readObserved || !writtenObserved {
		return telemetryByteCounters{}, fmt.Errorf("aggregate disk counters are incomplete")
	}
	return counters, nil
}
