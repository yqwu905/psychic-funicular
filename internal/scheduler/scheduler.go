// Package scheduler 实现 FIFO+优先级的单节点调度：把 PENDING 作业匹配到合适节点。
// 调度决策(Plan)是纯函数，便于单测与回放；Scheduler 负责周期调度并落库。
package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/store"
)

// Decision 是一次分配：把作业放到某节点并占用一组设备。
type Decision struct {
	JobID    string
	NodeID   string
	NodeName string
	Devices  []store.AllocDevice
}

// Scheduler 周期性地执行调度。
type Scheduler struct {
	store    store.Store
	log      *slog.Logger
	interval time.Duration
}

// New 创建一个 Scheduler。
func New(st store.Store, logger *slog.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{store: st, log: logger, interval: interval}
}

// Run 周期调度，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pass(ctx)
		}
	}
}

func (s *Scheduler) pass(ctx context.Context) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("scheduler: list nodes", "err", err)
		return
	}
	active, err := s.store.ListActiveJobs(ctx)
	if err != nil {
		s.log.Error("scheduler: list active jobs", "err", err)
		return
	}
	pending, err := s.store.ListPendingJobs(ctx)
	if err != nil {
		s.log.Error("scheduler: list pending jobs", "err", err)
		return
	}
	for _, d := range Plan(nodes, active, pending) {
		if err := s.store.AssignJob(ctx, d.JobID, d.NodeID, d.NodeName, d.Devices); err != nil {
			s.log.Warn("scheduler: assign failed", "job", d.JobID, "node", d.NodeName, "err", err)
			continue
		}
		s.log.Info("scheduled job", "job", d.JobID, "node", d.NodeName, "devices", len(d.Devices))
	}
}

// freeNode 是某节点的可用资源（容量减去在跑/已分配作业的占用）。
type freeNode struct {
	id, name, partition string
	cpus                uint32
	mem                 uint64
	devices             []store.AllocDevice
}

// Plan 计算一批分配决策（纯函数）。FIFO+优先级：按优先级降序、提交时间升序处理 pending。
func Plan(nodes []*store.Node, active, pending []*store.Job) []Decision {
	frees := buildFree(nodes, active)

	// 稳定的节点顺序，保证结果可复现。
	order := make([]*freeNode, 0, len(frees))
	for _, fn := range frees {
		order = append(order, fn)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].name < order[j].name })

	sorted := append([]*store.Job(nil), pending...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].SubmitAt.Before(sorted[j].SubmitAt)
	})

	var decisions []Decision
	for _, job := range sorted {
		for _, fn := range order {
			devs, ok := fit(job, fn)
			if !ok {
				continue
			}
			decisions = append(decisions, Decision{
				JobID: job.ID, NodeID: fn.id, NodeName: fn.name, Devices: devs,
			})
			consume(fn, job, devs)
			break
		}
	}
	return decisions
}

func buildFree(nodes []*store.Node, active []*store.Job) map[string]*freeNode {
	frees := make(map[string]*freeNode, len(nodes))
	for _, n := range nodes {
		if n.State != store.StateUp {
			continue
		}
		devs := make([]store.AllocDevice, 0, len(n.Devices))
		for _, d := range n.Devices {
			devs = append(devs, store.AllocDevice{Kind: d.Kind, Index: d.Index})
		}
		frees[n.ID] = &freeNode{id: n.ID, name: n.Name, partition: n.Partition,
			cpus: n.CPUs, mem: n.MemTotalBytes, devices: devs}
	}
	for _, job := range active {
		fn, ok := frees[job.NodeID]
		if !ok {
			continue
		}
		fn.cpus = subU32(fn.cpus, job.ReqCPUs)
		fn.mem = subU64(fn.mem, job.ReqMemBytes)
		fn.devices = removeDevices(fn.devices, job.Devices)
	}
	return frees
}

// fit 判断作业能否放入节点；可以则返回分配到的设备。
func fit(job *store.Job, fn *freeNode) ([]store.AllocDevice, bool) {
	if job.Partition != "" && job.Partition != fn.partition {
		return nil, false
	}
	if fn.cpus < job.ReqCPUs || fn.mem < job.ReqMemBytes {
		return nil, false
	}
	if uint32(len(fn.devices)) < job.ReqGPUs {
		return nil, false
	}
	picked := make([]store.AllocDevice, job.ReqGPUs)
	copy(picked, fn.devices[:job.ReqGPUs])
	return picked, true
}

// consume 在节点可用资源上扣减一次分配。
func consume(fn *freeNode, job *store.Job, devs []store.AllocDevice) {
	fn.cpus = subU32(fn.cpus, job.ReqCPUs)
	fn.mem = subU64(fn.mem, job.ReqMemBytes)
	fn.devices = removeDevices(fn.devices, devs)
}

func removeDevices(list, used []store.AllocDevice) []store.AllocDevice {
	if len(used) == 0 {
		return list
	}
	usedSet := make(map[store.AllocDevice]struct{}, len(used))
	for _, u := range used {
		usedSet[u] = struct{}{}
	}
	out := list[:0:0]
	for _, d := range list {
		if _, ok := usedSet[d]; ok {
			continue
		}
		out = append(out, d)
	}
	return out
}

func subU32(a, b uint32) uint32 {
	if b > a {
		return 0
	}
	return a - b
}

func subU64(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}
