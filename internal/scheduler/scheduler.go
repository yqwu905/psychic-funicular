// Package scheduler 实现 FIFO+优先级+老化 的调度，并支持 EASY backfill 与 gpu_type 匹配。
// 调度决策(Plan)是纯函数(见 plan.go)，便于单测与回放；Scheduler 负责周期调度并落库。
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/store"
)

// Scheduler 周期性地执行调度。
type Scheduler struct {
	store    store.Store
	log      *slog.Logger
	interval time.Duration
	policy   Policy
}

// New 创建一个 Scheduler。
func New(st store.Store, logger *slog.Logger, interval time.Duration, policy Policy) *Scheduler {
	return &Scheduler{store: st, log: logger, interval: interval, policy: policy}
}

// Run 周期调度，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	s.log.Info("scheduler started", "interval", s.interval,
		"backfill", s.policy.Backfill, "age_weight", s.policy.AgeWeight)
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
	for _, d := range Plan(time.Now(), nodes, active, pending, s.policy) {
		if err := s.store.AssignJob(ctx, d.JobID, d.NodeID, d.NodeName, d.Devices); err != nil {
			s.log.Warn("scheduler: assign failed", "job", d.JobID, "node", d.NodeName, "err", err)
			continue
		}
		s.log.Info("scheduled job", "job", d.JobID, "node", d.NodeName, "devices", len(d.Devices))
	}
}
