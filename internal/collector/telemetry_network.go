package collector

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// parseAnonymousIPv4Counters reads Linux's system-wide IP octet counters.
// The source contains protocol metric names only, never interface identity.
func parseAnonymousIPv4Counters(r io.Reader) (telemetryByteCounters, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		headers := strings.Fields(scanner.Text())
		if len(headers) < 2 || headers[0] != "IpExt:" {
			continue
		}
		if !scanner.Scan() {
			break
		}
		values := strings.Fields(scanner.Text())
		if len(values) != len(headers) || values[0] != "IpExt:" {
			return telemetryByteCounters{}, fmt.Errorf("aggregate IPv4 counters are malformed")
		}
		var (
			counters telemetryByteCounters
			inSeen   bool
			outSeen  bool
		)
		for i := 1; i < len(headers); i++ {
			if headers[i] != "InOctets" && headers[i] != "OutOctets" {
				continue
			}
			value, err := strconv.ParseUint(values[i], 10, 64)
			if err != nil {
				return telemetryByteCounters{}, fmt.Errorf("parse aggregate IPv4 counter %s: %w", headers[i], err)
			}
			if headers[i] == "InOctets" {
				counters.readBytes = value
				inSeen = true
			} else {
				counters.writeBytes = value
				outSeen = true
			}
		}
		if !inSeen || !outSeen {
			return telemetryByteCounters{}, fmt.Errorf("aggregate IPv4 counters are incomplete")
		}
		return counters, nil
	}
	if err := scanner.Err(); err != nil {
		return telemetryByteCounters{}, fmt.Errorf("read aggregate IPv4 counters: %w", err)
	}
	return telemetryByteCounters{}, fmt.Errorf("aggregate IPv4 counters are unavailable")
}

// parseAnonymousIPv6Counters reads Linux's system-wide IPv6 octet counters.
func parseAnonymousIPv6Counters(r io.Reader) (telemetryByteCounters, error) {
	var (
		counters telemetryByteCounters
		inSeen   bool
		outSeen  bool
	)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || (fields[0] != "Ip6InOctets" && fields[0] != "Ip6OutOctets") {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return telemetryByteCounters{}, fmt.Errorf("parse aggregate IPv6 counter %s: %w", fields[0], err)
		}
		if fields[0] == "Ip6InOctets" {
			counters.readBytes = value
			inSeen = true
		} else {
			counters.writeBytes = value
			outSeen = true
		}
	}
	if err := scanner.Err(); err != nil {
		return telemetryByteCounters{}, fmt.Errorf("read aggregate IPv6 counters: %w", err)
	}
	if !inSeen || !outSeen {
		return telemetryByteCounters{}, fmt.Errorf("aggregate IPv6 counters are incomplete")
	}
	return counters, nil
}

func addAnonymousByteCounters(left, right telemetryByteCounters) (telemetryByteCounters, error) {
	if math.MaxUint64-left.readBytes < right.readBytes ||
		math.MaxUint64-left.writeBytes < right.writeBytes {
		return telemetryByteCounters{}, fmt.Errorf("aggregate network counters overflow")
	}
	return telemetryByteCounters{
		readBytes:  left.readBytes + right.readBytes,
		writeBytes: left.writeBytes + right.writeBytes,
	}, nil
}
