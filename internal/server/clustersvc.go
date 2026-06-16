package server

import (
	"context"
	"log/slog"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clusterService 实现 ClusterService（供 skctl 等客户端调用）。
type clusterService struct {
	skipperv1.UnimplementedClusterServiceServer
	store store.Store
	log   *slog.Logger
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
