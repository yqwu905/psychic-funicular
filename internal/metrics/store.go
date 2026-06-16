// Package metrics 提供控制平面侧的近线指标存储：保存每个节点最近一次的指标快照。
// M1 仅保留最新值（够调度/展示/告警判断与 Prometheus 抓取用）；时间序列留待后续。
package metrics

import (
	"sync"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
)

// Store 是并发安全的「节点 id -> 最新快照」内存表。
type Store struct {
	mu   sync.RWMutex
	last map[string]*skipperv1.MetricsSnapshot
}

// New 创建一个空 Store。
func New() *Store {
	return &Store{last: make(map[string]*skipperv1.MetricsSnapshot)}
}

// Put 写入某节点的最新快照。
func (s *Store) Put(nodeID string, snap *skipperv1.MetricsSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[nodeID] = snap
}

// Get 读取某节点的最新快照。
func (s *Store) Get(nodeID string) (*skipperv1.MetricsSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.last[nodeID]
	return snap, ok
}

// Delete 移除某节点的快照。
func (s *Store) Delete(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.last, nodeID)
}
