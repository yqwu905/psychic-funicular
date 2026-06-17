// Package server 装配控制平面的 gRPC 服务、Prometheus 端点与后台任务。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/event"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/notify"
	"github.com/yqwu905/psychic-funicular/internal/scheduler"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"github.com/yqwu905/psychic-funicular/internal/transport"
	"google.golang.org/grpc"
)

// SSH 节点的默认回环监听地址与 agent 推送路径。
const (
	defaultRemoteListen = "127.0.0.1:7600"
	defaultRemotePath   = "/tmp/skipper-agent"
)

// Server 是控制平面实例。
type Server struct {
	cfg     config.ServerConfig
	log     *slog.Logger
	store   store.Store
	metrics *metrics.Store
	events  *notify.Engine
	grpc    *grpc.Server
	diag    *diagnoser // 经 SSH 纳管节点的失联诊断；未启用时为 nil
}

// New 创建一个 Server。
func New(cfg config.ServerConfig, logger *slog.Logger, st store.Store) *Server {
	return &Server{
		cfg: cfg, log: logger, store: st,
		metrics: metrics.New(),
		events:  notify.New(cfg.Notify, st, logger),
	}
}

// Run 启动 gRPC 监听、Prometheus 端点与失联巡检，阻塞直到 ctx 取消或出错。
func (s *Server) Run(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.Listen.GRPC)
	if err != nil {
		return err
	}

	jobLogs, err := newJobLogStore(s.cfg.Jobs.LogsDir)
	if err != nil {
		return err
	}

	s.grpc = grpc.NewServer()
	skipperv1.RegisterAgentServiceServer(s.grpc, &agentService{store: s.store, metricsStore: s.metrics, jobLogs: jobLogs, events: s.events, log: s.log})
	skipperv1.RegisterClusterServiceServer(s.grpc, &clusterService{store: s.store, metricsStore: s.metrics, jobLogs: jobLogs, log: s.log})

	sched := scheduler.New(s.store, s.log, s.cfg.Scheduler.Interval.Std(),
		scheduler.Policy{Backfill: s.cfg.Scheduler.Backfill, AgeWeight: s.cfg.Scheduler.AgeWeight})
	go sched.Run(ctx)

	detector := notify.NewDetector(s.store, s.metrics, s.events, s.cfg.Notify, s.log)
	go detector.Run(ctx)

	s.startSSHTunnels(ctx)

	if s.cfg.Diagnostics.Enabled && len(s.cfg.SSHNodes) > 0 {
		s.diag = newDiagnoser(s.store, s.events, s.cfg, s.log)
		s.log.Info("node failure diagnostics enabled",
			"after_down", s.cfg.Diagnostics.AfterDown.Std().String(),
			"cooldown", s.cfg.Diagnostics.Cooldown.Std().String())
	}

	go s.reapLoop(ctx)
	stopHTTP := s.startMetricsHTTP()

	// ctx 取消时优雅停机。
	go func() {
		<-ctx.Done()
		s.log.Info("shutting down")
		if stopHTTP != nil {
			stopHTTP()
		}
		s.grpc.GracefulStop()
	}()

	s.log.Info("grpc serving", "addr", s.cfg.Listen.GRPC)
	if err := s.grpc.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

// startMetricsHTTP 在配置了地址时启动 Prometheus /metrics 与 /healthz 端点。
// 返回一个关闭函数（未启用时为 nil）。
func (s *Server) startMetricsHTTP() func() {
	if s.cfg.Metrics.HTTP == "" {
		return nil
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(&promCollector{store: s.store, metrics: s.metrics})

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: s.cfg.Metrics.HTTP, Handler: mux}
	go func() {
		s.log.Info("metrics http serving", "addr", s.cfg.Metrics.HTTP, "path", "/metrics")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("metrics http exited", "err", err)
		}
	}()
	return func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}
}

// reapLoop 周期性地把长时间未心跳的节点标记为 DOWN。
func (s *Server) reapLoop(ctx context.Context) {
	interval := s.cfg.Heartbeat.ReapInterval.Std()
	timeout := s.cfg.Heartbeat.Timeout.Std()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			cutoff := now.Add(-timeout)
			nodes, err := s.store.ListNodes(ctx)
			if err != nil {
				s.log.Error("reap: list nodes", "err", err)
				continue
			}
			s.emitNodeDownEvents(ctx, nodes, cutoff)
			n, err := s.store.MarkStaleDown(ctx, cutoff)
			if err != nil {
				s.log.Error("reap stale nodes", "err", err)
				continue
			}
			if n > 0 {
				s.log.Warn("marked nodes down due to missed heartbeat", "count", n)
			}
			if s.diag != nil {
				s.diag.check(ctx, nodes, now)
			}
		}
	}
}

// emitNodeDownEvents 对即将判定失联的 UP 节点(心跳早于 cutoff)发 node.down 事件。
func (s *Server) emitNodeDownEvents(ctx context.Context, nodes []*store.Node, cutoff time.Time) {
	for _, n := range nodes {
		if n.State != store.StateUp || n.LastHeartbeat.IsZero() || !n.LastHeartbeat.Before(cutoff) {
			continue
		}
		s.events.Emit(ctx, event.Event{
			Type: event.TypeNodeDown, Severity: event.SevWarning, Source: n.Name,
			Summary:  fmt.Sprintf("节点 %s 心跳超时，判定失联", n.Name),
			DedupKey: "node-down|" + n.Name,
			Labels:   map[string]string{"node": n.Name},
		})
	}
}

// startSSHTunnels 为配置中的 SSH 节点建立反向转发隧道（适配仅开放 SSH 端口的容器）；
// 对开启 provision 的节点，在隧道就绪后自动分发并拉起 agent。
func (s *Server) startSSHTunnels(ctx context.Context) {
	if len(s.cfg.SSHNodes) == 0 {
		return
	}
	forwardTo := localDialTarget(s.cfg.Listen.GRPC)
	for _, sn := range s.cfg.SSHNodes {
		sn := sn
		t := transport.NewSSHTunnel(sshConfigFor(sn), forwardTo, s.log)
		go t.Run(ctx)
		if sn.Provision {
			go s.provisionWhenReady(ctx, t, sn)
		}
	}
	s.log.Info("ssh tunnels starting", "count", len(s.cfg.SSHNodes), "forward_to", forwardTo)
}

// provisionWhenReady 等隧道反向监听就绪后，把 agent 分发到节点并远程拉起（带退避重试）。
func (s *Server) provisionWhenReady(ctx context.Context, t *transport.SSHTunnel, sn config.SSHNodeConfig) {
	spec := provisionSpec(sn)
	if spec.LocalBin == "" {
		s.log.Error("provision enabled but agent_bin not set; skipping", "node", sn.Name)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-t.Ready():
	}
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if err := transport.Provision(ctx, sshConfigFor(sn), spec, s.log); err == nil {
			s.log.Info("agent provisioned", "node", sn.Name, "remote_path", spec.RemotePath, "attempt", attempt)
			return
		} else {
			s.log.Warn("agent provision failed, will retry", "node", sn.Name, "err", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// sshConfigFor 把 SSHNodeConfig 转成传输层 SSH 配置（补全回环监听默认值）。
func sshConfigFor(sn config.SSHNodeConfig) transport.Config {
	rl := sn.RemoteListen
	if rl == "" {
		rl = defaultRemoteListen
	}
	return transport.Config{
		Name: sn.Name, Addr: sn.Addr, User: sn.User,
		KeyPath: sn.Key, KnownHost: sn.KnownHost, RemoteListen: rl,
	}
}

// provisionSpec 由节点配置推导 agent 分发/拉起参数（补全路径默认值）。
// agent 以 RemoteListen 为 --server（隧道回环），以节点名为 --name 便于失联诊断对应。
func provisionSpec(sn config.SSHNodeConfig) transport.ProvisionSpec {
	remotePath := sn.RemotePath
	if remotePath == "" {
		remotePath = defaultRemotePath
	}
	rl := sn.RemoteListen
	if rl == "" {
		rl = defaultRemoteListen
	}
	return transport.ProvisionSpec{
		LocalBin:   sn.AgentBin,
		RemotePath: remotePath,
		ServerAddr: rl,
		NodeName:   sn.Name,
		Partition:  sn.Partition,
		ExtraArgs:  sn.AgentArgs,
		LogPath:    remotePath + ".log",
		PidPath:    remotePath + ".pid",
	}
}

// localDialTarget 把监听地址(可能是 :7443 / 0.0.0.0:7443)转成可拨号的本地地址。
func localDialTarget(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
