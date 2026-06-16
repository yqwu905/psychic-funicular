// Package collector 提供可插拔的资源采集器：CPU/内存/磁盘以及 GPU/NPU 设备。
// 每个采集器把自己负责的部分写入同一个 MetricsSnapshot；某个采集器失败不影响其余。
package collector

import (
	"context"
	"log/slog"
	"os/exec"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
)

// Collector 是单个采集器。Collect 把采到的指标填入 snap。
type Collector interface {
	Name() string
	Collect(ctx context.Context, snap *skipperv1.MetricsSnapshot) error
}

// Default 返回默认采集器集合：CPU/内存/磁盘恒定启用；
// GPU(nvidia-smi)/NPU(npu-smi) 仅在对应命令存在时启用，做到环境自适应。
func Default(log *slog.Logger) []Collector {
	cs := []Collector{&cpuCollector{}, &memCollector{}, &diskCollector{}}
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		cs = append(cs, &nvidiaCollector{log: log})
		log.Info("collector enabled", "name", "gpu.nvidia")
	}
	if _, err := exec.LookPath("npu-smi"); err == nil {
		cs = append(cs, &ascendCollector{log: log})
		log.Info("collector enabled", "name", "npu.ascend")
	}
	return cs
}

// CollectAll 运行全部采集器并汇总成一个快照；单个采集器出错仅记录日志。
func CollectAll(ctx context.Context, cs []Collector, log *slog.Logger) *skipperv1.MetricsSnapshot {
	snap := &skipperv1.MetricsSnapshot{TimestampUnix: time.Now().Unix()}
	for _, c := range cs {
		if err := c.Collect(ctx, snap); err != nil {
			log.Warn("collector failed", "name", c.Name(), "err", err)
		}
	}
	return snap
}

// ResourcesFromSnapshot 从快照提取静态资源清单（用于节点注册/库存）。
func ResourcesFromSnapshot(snap *skipperv1.MetricsSnapshot) *skipperv1.Resources {
	r := &skipperv1.Resources{}
	if snap.GetCpu() != nil {
		r.Cpus = snap.GetCpu().GetCores()
	}
	if snap.GetMem() != nil {
		r.MemTotalBytes = snap.GetMem().GetTotalBytes()
	}
	for _, d := range snap.GetDevices() {
		r.Devices = append(r.Devices, &skipperv1.Device{
			Kind:          d.GetKind(),
			Vendor:        d.GetVendor(),
			Index:         d.GetIndex(),
			Uuid:          d.GetUuid(),
			MemTotalBytes: d.GetMemTotalBytes(),
			Name:          d.GetName(),
		})
	}
	return r
}
