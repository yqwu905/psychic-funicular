// Package server 装配控制平面的 gRPC 服务、Prometheus 端点与后台任务。
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/scheduler"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc"
)

// Server 是控制平面实例。
type Server struct {
	cfg     config.ServerConfig
	log     *slog.Logger
	store   store.Store
	metrics *metrics.Store
	grpc    *grpc.Server
}

// New 创建一个 Server。
func New(cfg config.ServerConfig, logger *slog.Logger, st store.Store) *Server {
	return &Server{cfg: cfg, log: logger, store: st, metrics: metrics.New()}
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
	skipperv1.RegisterAgentServiceServer(s.grpc, &agentService{store: s.store, metricsStore: s.metrics, jobLogs: jobLogs, log: s.log})
	skipperv1.RegisterClusterServiceServer(s.grpc, &clusterService{store: s.store, metricsStore: s.metrics, jobLogs: jobLogs, log: s.log})

	sched := scheduler.New(s.store, s.log, s.cfg.Scheduler.Interval.Std())
	go sched.Run(ctx)

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
			n, err := s.store.MarkStaleDown(ctx, time.Now().Add(-timeout))
			if err != nil {
				s.log.Error("reap stale nodes", "err", err)
				continue
			}
			if n > 0 {
				s.log.Warn("marked nodes down due to missed heartbeat", "count", n)
			}
		}
	}
}
