// Package server 装配控制平面的 gRPC 服务与后台任务。
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc"
)

// Server 是控制平面实例。
type Server struct {
	cfg   config.ServerConfig
	log   *slog.Logger
	store store.Store
	grpc  *grpc.Server
}

// New 创建一个 Server。
func New(cfg config.ServerConfig, logger *slog.Logger, st store.Store) *Server {
	return &Server{cfg: cfg, log: logger, store: st}
}

// Run 启动 gRPC 监听与失联巡检，阻塞直到 ctx 取消或出错。
func (s *Server) Run(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.Listen.GRPC)
	if err != nil {
		return err
	}

	s.grpc = grpc.NewServer()
	skipperv1.RegisterAgentServiceServer(s.grpc, &agentService{store: s.store, log: s.log})
	skipperv1.RegisterClusterServiceServer(s.grpc, &clusterService{store: s.store, log: s.log})

	go s.reapLoop(ctx)

	// ctx 取消时优雅停机。
	go func() {
		<-ctx.Done()
		s.log.Info("shutting down")
		s.grpc.GracefulStop()
	}()

	s.log.Info("grpc serving", "addr", s.cfg.Listen.GRPC)
	if err := s.grpc.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
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
