// Package agent 实现节点代理：采集基础资源、向控制平面注册并周期心跳。
// M0 仅采集 CPU 核数与内存总量；GPU/NPU 等真实采集器在 M1 引入。
package agent

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

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

	conn, err := grpc.NewClient(cfg.Server.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := skipperv1.NewAgentServiceClient(conn)

	nodeID, err := register(ctx, client, cfg, name, logger)
	if err != nil {
		return err
	}

	interval := cfg.Collectors.Interval.Std()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("agent stopping")
			return nil
		case <-ticker.C:
			resp, err := client.Heartbeat(ctx, &skipperv1.HeartbeatRequest{
				NodeId:    nodeID,
				Resources: collect(),
			})
			if err != nil {
				logger.Warn("heartbeat failed", "err", err)
				continue
			}
			if resp.GetShouldReregister() {
				logger.Info("server requested re-register")
				if id, err := register(ctx, client, cfg, name, logger); err != nil {
					logger.Warn("re-register failed", "err", err)
				} else {
					nodeID = id
				}
			}
		}
	}
}

// register 带简单重试地向控制平面注册节点。
func register(ctx context.Context, client skipperv1.AgentServiceClient, cfg config.AgentConfig, name string, logger *slog.Logger) (string, error) {
	req := &skipperv1.RegisterNodeRequest{
		Name:         name,
		Partition:    cfg.Node.Partition,
		Resources:    collect(),
		Labels:       cfg.Node.Labels,
		AgentVersion: version.Version,
	}
	backoff := time.Second
	for {
		resp, err := client.RegisterNode(ctx, req)
		if err == nil {
			logger.Info("registered with server", "node_id", resp.GetNodeId(), "name", name, "server", cfg.Server.Addr)
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

// collect 采集当前节点的基础资源。
func collect() *skipperv1.Resources {
	return &skipperv1.Resources{
		Cpus:          uint32(runtime.NumCPU()),
		MemTotalBytes: memTotalBytes(),
	}
}

// memTotalBytes 从 /proc/meminfo 读取 MemTotal（字节）；非 Linux 或失败时返回 0。
func memTotalBytes() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line) // MemTotal:  16384256 kB
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
