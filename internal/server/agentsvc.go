package server

import (
	"context"
	"log/slog"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// agentService 实现 AgentService（供节点 Agent 调用）。
type agentService struct {
	skipperv1.UnimplementedAgentServiceServer
	store        store.Store
	metricsStore *metrics.Store
	log          *slog.Logger
}

func (s *agentService) RegisterNode(ctx context.Context, req *skipperv1.RegisterNodeRequest) (*skipperv1.RegisterNodeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "node name is required")
	}
	n := &store.Node{
		ID:            store.NewID(),
		Name:          req.GetName(),
		Partition:     req.GetPartition(),
		State:         store.StateUp,
		Addr:          peerAddr(ctx),
		Labels:        req.GetLabels(),
		AgentVersion:  req.GetAgentVersion(),
		LastHeartbeat: time.Now(),
	}
	resourcesFromProto(n, req.GetResources())

	id, err := s.store.RegisterNode(ctx, n)
	if err != nil {
		s.log.Error("register node failed", "name", n.Name, "err", err)
		return nil, status.Error(codes.Internal, "register node failed")
	}
	s.log.Info("node registered", "id", id, "name", n.Name, "partition", n.Partition,
		"cpus", n.CPUs, "devices", len(n.Devices), "addr", n.Addr, "version", n.AgentVersion)
	return &skipperv1.RegisterNodeResponse{NodeId: id}, nil
}

func (s *agentService) Heartbeat(ctx context.Context, req *skipperv1.HeartbeatRequest) (*skipperv1.HeartbeatResponse, error) {
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	n := &store.Node{ID: req.GetNodeId()}
	resourcesFromProto(n, req.GetResources())

	found, err := s.store.Heartbeat(ctx, n, time.Now())
	if err != nil {
		s.log.Error("heartbeat failed", "node_id", n.ID, "err", err)
		return nil, status.Error(codes.Internal, "heartbeat failed")
	}
	if !found {
		s.log.Warn("heartbeat from unknown node, asking re-register", "node_id", n.ID)
		return &skipperv1.HeartbeatResponse{Ok: false, ShouldReregister: true}, nil
	}
	return &skipperv1.HeartbeatResponse{Ok: true}, nil
}

func (s *agentService) ReportMetrics(ctx context.Context, req *skipperv1.ReportMetricsRequest) (*skipperv1.ReportMetricsResponse, error) {
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	snap := req.GetSnapshot()

	// 从快照刷新静态库存(核数/内存/设备)并充当存活信号。
	n := &store.Node{ID: req.GetNodeId()}
	nodeFromSnapshot(n, snap)
	found, err := s.store.Heartbeat(ctx, n, time.Now())
	if err != nil {
		s.log.Error("report metrics failed", "node_id", n.ID, "err", err)
		return nil, status.Error(codes.Internal, "report metrics failed")
	}
	if !found {
		s.log.Warn("metrics from unknown node, asking re-register", "node_id", n.ID)
		return &skipperv1.ReportMetricsResponse{Ok: false, ShouldReregister: true}, nil
	}
	s.metricsStore.Put(req.GetNodeId(), snap)
	return &skipperv1.ReportMetricsResponse{Ok: true}, nil
}

// peerAddr 从 gRPC 上下文提取对端地址。
func peerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return ""
}
