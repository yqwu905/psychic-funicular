package server

import (
	"context"
	"log/slog"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clusterService 实现 ClusterService（供 skctl 等客户端调用）。
type clusterService struct {
	skipperv1.UnimplementedClusterServiceServer
	store        store.Store
	metricsStore *metrics.Store
	log          *slog.Logger
}

func (s *clusterService) ListNodes(ctx context.Context, _ *skipperv1.ListNodesRequest) (*skipperv1.ListNodesResponse, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("list nodes failed", "err", err)
		return nil, status.Error(codes.Internal, "list nodes failed")
	}
	resp := &skipperv1.ListNodesResponse{Nodes: make([]*skipperv1.Node, 0, len(nodes))}
	for _, n := range nodes {
		resp.Nodes = append(resp.Nodes, nodeToProto(n))
	}
	return resp, nil
}

func (s *clusterService) ListMetrics(ctx context.Context, _ *skipperv1.ListMetricsRequest) (*skipperv1.ListMetricsResponse, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("list metrics failed", "err", err)
		return nil, status.Error(codes.Internal, "list metrics failed")
	}
	resp := &skipperv1.ListMetricsResponse{Nodes: make([]*skipperv1.NodeMetrics, 0, len(nodes))}
	for _, n := range nodes {
		snap, _ := s.metricsStore.Get(n.ID)
		resp.Nodes = append(resp.Nodes, &skipperv1.NodeMetrics{
			NodeId:    n.ID,
			NodeName:  n.Name,
			Partition: n.Partition,
			State:     n.State,
			Snapshot:  snap,
		})
	}
	return resp, nil
}
