// Package agent 实现节点代理：采集资源、注册、周期上报指标、拉取并执行作业。
package agent

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/collector"
	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// nodeRef 在指标循环与作业轮询循环间共享当前 node id（注册/重注册时更新）。
type nodeRef struct {
	mu sync.Mutex
	id string
}

func (r *nodeRef) get() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

func (r *nodeRef) set(s string) {
	r.mu.Lock()
	r.id = s
	r.mu.Unlock()
}

// Run 启动 Agent，阻塞直到 ctx 取消。
func Run(ctx context.Context, cfg config.AgentConfig, logger *slog.Logger) error {
	name := cfg.Node.Name
	if name == "" {
		if hn, err := os.Hostname(); err == nil {
			name = hn
		} else {
			name = "unknown-node"
		}
	}

	collectors := collector.Default(logger)
	logger.Info("collectors initialized", "count", len(collectors))

	conn, err := grpc.NewClient(cfg.Server.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := skipperv1.NewAgentServiceClient(conn)

	nodeID, err := register(ctx, client, collectors, cfg, name, logger)
	if err != nil {
		return err
	}
	nref := &nodeRef{id: nodeID}

	exec := newExecutor(client, logger)
	go pollLoop(ctx, client, nref, exec, cfg.Jobs.PollInterval.Std(), logger)

	ticker := time.NewTicker(cfg.Collectors.Interval.Std())
	defer ticker.Stop()
	// 注册后立即上报一次，避免启动到首个 tick 的空窗，再按周期上报。
	for {
		snap := collector.CollectAll(ctx, collectors, logger)
		resp, err := client.ReportMetrics(ctx, &skipperv1.ReportMetricsRequest{
			NodeId:   nref.get(),
			Snapshot: snap,
		})
		switch {
		case err != nil:
			logger.Warn("report metrics failed", "err", err)
		case resp.GetShouldReregister():
			logger.Info("server requested re-register")
			if id, err := register(ctx, client, collectors, cfg, name, logger); err != nil {
				logger.Warn("re-register failed", "err", err)
			} else {
				nref.set(id)
			}
		}

		select {
		case <-ctx.Done():
			logger.Info("agent stopping")
			return nil
		case <-ticker.C:
		}
	}
}

// pollLoop 周期拉取本节点待执行/待取消的作业并交给执行器。
func pollLoop(ctx context.Context, client skipperv1.AgentServiceClient, nref *nodeRef, exec *executor, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodeID := nref.get()
			if nodeID == "" {
				continue
			}
			resp, err := client.PollAssignments(ctx, &skipperv1.PollAssignmentsRequest{NodeId: nodeID})
			if err != nil {
				logger.Warn("poll assignments failed", "err", err)
				continue
			}
			for _, a := range resp.GetRun() {
				logger.Info("starting job", "job", a.GetJobId(), "devices", len(a.GetDevices()))
				exec.start(a)
			}
			for _, id := range resp.GetCancelJobIds() {
				logger.Info("cancelling job", "job", id)
				exec.cancel(id)
			}
		}
	}
}

// register 带指数退避地向控制平面注册节点；资源清单由一次采样推导。
func register(ctx context.Context, client skipperv1.AgentServiceClient, collectors []collector.Collector, cfg config.AgentConfig, name string, logger *slog.Logger) (string, error) {
	snap := collector.CollectAll(ctx, collectors, logger)
	req := &skipperv1.RegisterNodeRequest{
		Name:         name,
		Partition:    cfg.Node.Partition,
		Resources:    collector.ResourcesFromSnapshot(snap),
		Labels:       cfg.Node.Labels,
		AgentVersion: version.Version,
	}
	backoff := time.Second
	for {
		resp, err := client.RegisterNode(ctx, req)
		if err == nil {
			logger.Info("registered with server", "node_id", resp.GetNodeId(), "name", name,
				"server", cfg.Server.Addr, "cpus", req.GetResources().GetCpus(), "devices", len(req.GetResources().GetDevices()))
			return resp.GetNodeId(), nil
		}
		logger.Warn("register failed, retrying", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 16*time.Second {
			backoff *= 2
		}
	}
}
