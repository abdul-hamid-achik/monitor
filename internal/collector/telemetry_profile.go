package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/abdul-hamid-achik/monitor/internal/capability"
)

type collectionStage string

const (
	collectionCPUUsage         collectionStage = "cpu_usage"
	collectionCPUPerCore       collectionStage = "cpu_per_core"
	collectionCPUTopology      collectionStage = "cpu_topology"
	collectionCPULoad          collectionStage = "cpu_load"
	collectionMemoryVirtual    collectionStage = "memory_virtual"
	collectionMemorySwap       collectionStage = "memory_swap"
	collectionCgroup           collectionStage = "cgroup"
	collectionTemperature      collectionStage = "temperature"
	collectionNetworkAggregate collectionStage = "network_aggregate"
	collectionDiskPartitions   collectionStage = "disk_partitions"
	collectionDiskAggregate    collectionStage = "disk_aggregate"
	collectionProcesses        collectionStage = "processes"
	collectionHostIdentity     collectionStage = "host_identity"
	collectionHistory          collectionStage = "history"
)

type telemetryVirtualMemory struct {
	usedBytes      uint64
	availableBytes uint64
	usagePercent   float64
	pressure       float64
}

type telemetrySwap struct {
	usedBytes  uint64
	totalBytes uint64
}

type telemetryByteCounters struct {
	readBytes  uint64
	writeBytes uint64
}

type telemetryCounterSample struct {
	counters telemetryByteCounters
	at       time.Time
}

type telemetrySourceSet struct {
	now           func() time.Time
	cpuUsage      func(context.Context) (float64, error)
	loadOne       func(context.Context) (float64, error)
	virtualMemory func(context.Context) (telemetryVirtualMemory, error)
	swap          func(context.Context) (telemetrySwap, error)
	network       func(context.Context) (telemetryCounterSample, error)
	disk          func(context.Context) (telemetryCounterSample, error)
}

type telemetryRateState struct {
	network       telemetryByteCounters
	networkAt     time.Time
	networkPrimed bool
	disk          telemetryByteCounters
	diskAt        time.Time
	diskPrimed    bool
}

func newTelemetrySourceSet(loadAverage func(context.Context) (*load.AvgStat, error)) telemetrySourceSet {
	clock := time.Now
	return telemetrySourceSet{
		now: clock,
		cpuUsage: func(ctx context.Context) (float64, error) {
			percent, err := cpu.PercentWithContext(ctx, 0, false)
			if err != nil {
				return 0, err
			}
			if len(percent) == 0 {
				return 0, fmt.Errorf("CPU usage collector returned no samples")
			}
			return percent[0], nil
		},
		loadOne: func(ctx context.Context) (float64, error) {
			avg, err := loadAverage(ctx)
			if err != nil {
				return 0, err
			}
			if avg == nil {
				return 0, fmt.Errorf("load average collector returned no sample")
			}
			return avg.Load1, nil
		},
		virtualMemory: func(ctx context.Context) (telemetryVirtualMemory, error) {
			vm, err := mem.VirtualMemoryWithContext(ctx)
			if err != nil {
				return telemetryVirtualMemory{}, err
			}
			pressure := 0.0
			if vm.Total > 0 {
				pressure = float64(vm.Used) / float64(vm.Total) * 100
			}
			return telemetryVirtualMemory{
				usedBytes:      vm.Used,
				availableBytes: vm.Available,
				usagePercent:   vm.UsedPercent,
				pressure:       pressure,
			}, nil
		},
		swap: func(ctx context.Context) (telemetrySwap, error) {
			swap, err := mem.SwapMemoryWithContext(ctx)
			if err != nil {
				return telemetrySwap{}, err
			}
			return telemetrySwap{usedBytes: swap.Used, totalBytes: swap.Total}, nil
		},
		network: func(ctx context.Context) (telemetryCounterSample, error) {
			startedAt := clock()
			counters, err := telemetryNetworkCounters(ctx)
			finishedAt := clock()
			if err != nil {
				return telemetryCounterSample{}, err
			}
			return telemetryCounterSample{
				counters: counters,
				at:       midpoint(startedAt, finishedAt),
			}, nil
		},
		disk: func(ctx context.Context) (telemetryCounterSample, error) {
			startedAt := clock()
			counters, err := telemetryDiskCounters(ctx)
			finishedAt := clock()
			if err != nil {
				return telemetryCounterSample{}, err
			}
			return telemetryCounterSample{
				counters: counters,
				at:       midpoint(startedAt, finishedAt),
			}, nil
		},
	}
}

func midpoint(start, end time.Time) time.Time {
	return start.Add(end.Sub(start) / 2)
}

func (c *Collector) observeCollection(stage collectionStage) {
	if c.collectionObserver != nil {
		c.collectionObserver(stage)
	}
}

func (c *Collector) captureTelemetry(ctx context.Context) (SystemInfo, error) {
	if err := c.capabilities.Require(capability.SystemMetrics); err != nil {
		support := c.capabilities.SupportFor(capability.SystemMetrics)
		c.info = SystemInfo{Capture: telemetryCapabilityStatus(support, "system metrics")}
		c.mu.Lock()
		c.published = c.info
		info := c.published
		c.mu.Unlock()
		return info, fmt.Errorf("telemetry system metrics are unavailable")
	}

	sampledAt := c.telemetrySources.now()
	c.info = SystemInfo{
		LastUpdate: sampledAt,
		Capture:    metricStatus(MetricObserved, ""),
		CPU: CPUInfo{
			LastUpdate:   sampledAt,
			MetricStates: make(map[string]MetricStatus),
		},
		Memory: MemoryInfo{
			LastUpdate:   sampledAt,
			MetricStates: make(map[string]MetricStatus),
		},
		Network: NetworkInfo{
			LastUpdate:   sampledAt,
			MetricStates: make(map[string]MetricStatus),
		},
		Disk: DiskInfo{
			LastUpdate:   sampledAt,
			MetricStates: make(map[string]MetricStatus),
		},
	}

	c.collectTelemetryCPU(ctx)
	c.collectTelemetryMemory(ctx)
	c.collectTelemetryNetwork(ctx)
	c.collectTelemetryDisk(ctx)
	return c.publishCurrent(), nil
}

func (c *Collector) collectTelemetryCPU(ctx context.Context) {
	c.observeCollection(collectionCPUUsage)
	if usage, err := c.telemetrySources.cpuUsage(ctx); err != nil {
		c.info.CPU.MetricStates[metricCPUUsage] = metricStatus(MetricUnavailable, "telemetry CPU usage is unavailable")
	} else {
		c.info.CPU.UsagePercent = usage
		c.info.CPU.MetricStates[metricCPUUsage] = metricStatus(MetricObserved, "")
	}

	c.observeCollection(collectionCPULoad)
	loadSupport := c.capabilities.SupportFor(capability.CPULoadAverage)
	switch {
	case loadSupport.State != capability.Supported:
		c.info.CPU.MetricStates[metricCPULoad] = telemetryCapabilityStatus(loadSupport, "load average")
	case c.telemetrySources.loadOne == nil:
		c.info.CPU.MetricStates[metricCPULoad] = metricStatus(MetricUnavailable, "telemetry load average is unavailable")
	default:
		loadOne, err := c.telemetrySources.loadOne(ctx)
		if err != nil {
			c.info.CPU.MetricStates[metricCPULoad] = metricStatus(MetricUnavailable, "telemetry load average is unavailable")
		} else {
			c.info.CPU.LoadAvg1 = loadOne
			c.info.CPU.MetricStates[metricCPULoad] = metricStatus(MetricObserved, "")
		}
	}
}

func telemetryCapabilityStatus(support capability.Support, family string) MetricStatus {
	switch support.State {
	case capability.Unsupported:
		return metricStatus(MetricUnsupported, "telemetry "+family+" is unsupported")
	case capability.Unavailable:
		return metricStatus(MetricUnavailable, "telemetry "+family+" is unavailable")
	default:
		return metricStatus(MetricObserved, "")
	}
}

func (c *Collector) collectTelemetryMemory(ctx context.Context) {
	c.observeCollection(collectionMemoryVirtual)
	if vm, err := c.telemetrySources.virtualMemory(ctx); err != nil {
		c.info.Memory.MetricStates[metricMemoryVirtual] = metricStatus(MetricUnavailable, "telemetry memory is unavailable")
	} else {
		c.info.Memory.UsedBytes = vm.usedBytes
		c.info.Memory.AvailableBytes = vm.availableBytes
		c.info.Memory.UsagePercent = vm.usagePercent
		c.info.Memory.MemoryPressure = vm.pressure
		c.info.Memory.MetricStates[metricMemoryVirtual] = metricStatus(MetricObserved, "")
	}

	c.observeCollection(collectionMemorySwap)
	if swap, err := c.telemetrySources.swap(ctx); err != nil {
		c.info.Memory.MetricStates[metricMemorySwap] = metricStatus(MetricUnavailable, "telemetry swap is unavailable")
	} else {
		c.info.Memory.SwapUsed = swap.usedBytes
		// SwapTotal is retained only to evaluate the identity-free
		// swap_pressure alert; it is not part of the telemetry envelope.
		c.info.Memory.SwapTotal = swap.totalBytes
		c.info.Memory.MetricStates[metricMemorySwap] = metricStatus(MetricObserved, "")
	}
}

func (c *Collector) collectTelemetryNetwork(ctx context.Context) {
	c.observeCollection(collectionNetworkAggregate)
	current, err := c.telemetrySources.network(ctx)
	if err != nil || current.at.IsZero() {
		c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricUnavailable, "telemetry network rate is unavailable")
		return
	}
	if c.telemetryState.networkPrimed {
		elapsed := current.at.Sub(c.telemetryState.networkAt).Seconds()
		if elapsed > 0 {
			c.info.Network.BytesRecvPerSec = perSecond(c.telemetryState.network.readBytes, current.counters.readBytes, elapsed)
			c.info.Network.BytesSentPerSec = perSecond(c.telemetryState.network.writeBytes, current.counters.writeBytes, elapsed)
			c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricObserved, "")
		} else {
			c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricUnavailable, "telemetry sampling interval was not positive")
		}
	} else {
		c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricUnavailable, "telemetry rate needs a prior sample")
	}
	c.telemetryState.network = current.counters
	c.telemetryState.networkAt = current.at
	c.telemetryState.networkPrimed = true
}

func (c *Collector) collectTelemetryDisk(ctx context.Context) {
	c.observeCollection(collectionDiskAggregate)
	current, err := c.telemetrySources.disk(ctx)
	if err != nil || current.at.IsZero() {
		c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricUnavailable, "telemetry disk rate is unavailable")
	} else {
		if c.telemetryState.diskPrimed {
			elapsed := current.at.Sub(c.telemetryState.diskAt).Seconds()
			if elapsed > 0 {
				c.info.Disk.ReadPerSec = perSecond(c.telemetryState.disk.readBytes, current.counters.readBytes, elapsed)
				c.info.Disk.WritePerSec = perSecond(c.telemetryState.disk.writeBytes, current.counters.writeBytes, elapsed)
				c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricObserved, "")
			} else {
				c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricUnavailable, "telemetry sampling interval was not positive")
			}
		} else {
			c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricUnavailable, "telemetry rate needs a prior sample")
		}
		c.telemetryState.disk = current.counters
		c.telemetryState.diskAt = current.at
		c.telemetryState.diskPrimed = true
	}
}
