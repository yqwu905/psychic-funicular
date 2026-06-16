package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/event"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

// Detector 在控制平面侧周期扫描最新指标，产出 disk.full / device.idle 等事件。
// 集中检测的好处：复用已收到的指标与作业分配，阈值与状态单点维护，无需改 Agent。
type Detector struct {
	store   store.Store
	metrics *metrics.Store
	engine  *Engine
	cfg     config.NotifyConfig
	log     *slog.Logger

	diskFull  map[string]bool      // node|mount -> 当前是否超阈值
	idleSince map[string]time.Time // node|kind|index -> 首次空闲时刻
}

// NewDetector 创建检测器。
func NewDetector(st store.Store, m *metrics.Store, eng *Engine, cfg config.NotifyConfig, log *slog.Logger) *Detector {
	return &Detector{
		store: st, metrics: m, engine: eng, cfg: cfg, log: log,
		diskFull:  make(map[string]bool),
		idleSince: make(map[string]time.Time),
	}
}

// Run 周期扫描，阻塞直到 ctx 取消。
func (d *Detector) Run(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Detector.Interval.Std())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.scan(ctx)
		}
	}
}

func (d *Detector) scan(ctx context.Context) {
	nodes, err := d.store.ListNodes(ctx)
	if err != nil {
		return
	}
	active, _ := d.store.ListActiveJobs(ctx)
	for _, n := range nodes {
		if n.State != store.StateUp {
			continue
		}
		snap, ok := d.metrics.Get(n.ID)
		if !ok || snap == nil {
			continue
		}
		d.checkDisks(ctx, n, snap)
		d.checkDevices(ctx, n, snap, active)
	}
}

func (d *Detector) checkDisks(ctx context.Context, n *store.Node, snap *skipperv1.MetricsSnapshot) {
	threshold := d.cfg.Detector.DiskThreshold * 100
	for _, disk := range snap.GetDisks() {
		key := n.Name + "|" + disk.GetMount()
		full := disk.GetUsedPercent() >= threshold
		switch {
		case full && !d.diskFull[key]:
			d.diskFull[key] = true
			d.engine.Emit(ctx, event.Event{
				Type: event.TypeDiskFull, Severity: event.SevWarning, Source: n.Name,
				Summary:  fmt.Sprintf("节点 %s 磁盘 %s 使用率 %.0f%% 超过阈值", n.Name, disk.GetMount(), disk.GetUsedPercent()),
				DedupKey: key,
				Labels: map[string]string{"node": n.Name, "mount": disk.GetMount(),
					"used_pct": fmt.Sprintf("%.0f", disk.GetUsedPercent())},
			})
		case !full && d.diskFull[key]:
			delete(d.diskFull, key)
			d.engine.Emit(ctx, event.Event{
				Type: event.TypeDiskRecovered, Severity: event.SevInfo, Source: n.Name,
				Summary:  fmt.Sprintf("节点 %s 磁盘 %s 使用率回落到 %.0f%%", n.Name, disk.GetMount(), disk.GetUsedPercent()),
				DedupKey: key,
				Labels:   map[string]string{"node": n.Name, "mount": disk.GetMount()},
			})
		}
	}
}

func (d *Detector) checkDevices(ctx context.Context, n *store.Node, snap *skipperv1.MetricsSnapshot, active []*store.Job) {
	idleUtil := d.cfg.Detector.DeviceIdleUtil * 100
	dur := d.cfg.Detector.DeviceIdleDuration.Std()
	now := time.Now()
	for _, dev := range snap.GetDevices() {
		key := fmt.Sprintf("%s|%s|%d", n.Name, dev.GetKind(), dev.GetIndex())
		if dev.GetUtilization() >= idleUtil {
			delete(d.idleSince, key)
			continue
		}
		first, ok := d.idleSince[key]
		if !ok {
			d.idleSince[key] = now
			continue
		}
		if now.Sub(first) < dur {
			continue
		}
		// 空置达标：区分「已分配但闲置」与「空闲未分配」，通知对象不同。
		allocated, owner := allocationOf(n.ID, dev.GetKind(), dev.GetIndex(), active)
		labels := map[string]string{
			"node": n.Name, "kind": dev.GetKind(),
			"index": fmt.Sprintf("%d", dev.GetIndex()), "allocated": fmt.Sprintf("%t", allocated),
		}
		var summary string
		if allocated {
			labels["owner"] = owner
			summary = fmt.Sprintf("节点 %s 的 %s%d 已分配给 %s 但空置超过 %s，请确认是否释放",
				n.Name, dev.GetKind(), dev.GetIndex(), owner, dur)
		} else {
			summary = fmt.Sprintf("节点 %s 的 %s%d 空闲超过 %s（可用资源）",
				n.Name, dev.GetKind(), dev.GetIndex(), dur)
		}
		d.engine.Emit(ctx, event.Event{
			Type: event.TypeDeviceIdle, Severity: event.SevWarning, Source: n.Name,
			Summary: summary, DedupKey: key, Labels: labels,
		})
	}
}

// allocationOf 判断某设备是否被某活跃作业占用，返回(是否占用, 属主)。
func allocationOf(nodeID, kind string, index uint32, active []*store.Job) (bool, string) {
	for _, j := range active {
		if j.NodeID != nodeID {
			continue
		}
		for _, dev := range j.Devices {
			if dev.Kind == kind && dev.Index == index {
				return true, j.Owner
			}
		}
	}
	return false, ""
}
