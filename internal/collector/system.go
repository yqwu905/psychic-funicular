package collector

import (
	"context"
	"runtime"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
)

// cpuCollector 采集整体 CPU 使用率与负载。
type cpuCollector struct{}

func (cpuCollector) Name() string { return "cpu" }

func (cpuCollector) Collect(ctx context.Context, snap *skipperv1.MetricsSnapshot) error {
	c := &skipperv1.CPUStats{Cores: uint32(runtime.NumCPU())}
	// interval=0：返回自上次调用以来的使用率，适合周期采样。
	if pct, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pct) > 0 {
		c.Utilization = pct[0]
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		c.Load1, c.Load5, c.Load15 = l.Load1, l.Load5, l.Load15
	}
	snap.Cpu = c
	return nil
}

// memCollector 采集内存使用。
type memCollector struct{}

func (memCollector) Name() string { return "mem" }

func (memCollector) Collect(ctx context.Context, snap *skipperv1.MetricsSnapshot) error {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return err
	}
	snap.Mem = &skipperv1.MemStats{
		TotalBytes:     vm.Total,
		UsedBytes:      vm.Used,
		AvailableBytes: vm.Available,
		UsedPercent:    vm.UsedPercent,
	}
	return nil
}

// diskCollector 按挂载点采集磁盘使用。
type diskCollector struct{}

func (diskCollector) Name() string { return "disk" }

func (diskCollector) Collect(ctx context.Context, snap *skipperv1.MetricsSnapshot) error {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		if _, ok := seen[p.Mountpoint]; ok {
			continue
		}
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Mountpoint] = struct{}{}
		snap.Disks = append(snap.Disks, &skipperv1.DiskStats{
			Mount:             p.Mountpoint,
			Fstype:            p.Fstype,
			TotalBytes:        u.Total,
			UsedBytes:         u.Used,
			UsedPercent:       u.UsedPercent,
			InodesUsedPercent: u.InodesUsedPercent,
		})
	}
	return nil
}
