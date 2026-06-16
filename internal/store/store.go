// Package store 定义持久化模型与接口。M0 仅含节点；作业/事件等后续里程碑加入。
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// 节点状态常量。
const (
	StateUp    = "UP"
	StateDown  = "DOWN"
	StateDrain = "DRAIN"
)

// Device 是一块加速设备（GPU/NPU）。
type Device struct {
	Kind          string `json:"kind"`
	Vendor        string `json:"vendor"`
	Index         uint32 `json:"index"`
	UUID          string `json:"uuid"`
	MemTotalBytes uint64 `json:"mem_total_bytes"`
}

// Node 是一个计算节点的持久化视图。
type Node struct {
	ID            string
	Name          string
	Partition     string
	State         string
	Addr          string
	CPUs          uint32
	MemTotalBytes uint64
	Devices       []Device
	Labels        map[string]string
	AgentVersion  string
	LastHeartbeat time.Time
}

// Store 是持久化接口，便于 SQLite 与后续 PostgreSQL 实现互换。
type Store interface {
	// RegisterNode 按 name upsert：若同名节点已存在则更新并保留其原 id，
	// 否则以 n.ID 新建。返回最终生效的 node id。
	RegisterNode(ctx context.Context, n *Node) (string, error)
	// Heartbeat 刷新 n.ID 节点的资源、状态(置 UP)与心跳时间；found 表示节点是否存在。
	Heartbeat(ctx context.Context, n *Node, t time.Time) (found bool, err error)
	// ListNodes 返回全部节点。
	ListNodes(ctx context.Context) ([]*Node, error)
	// MarkStaleDown 把 last_heartbeat 早于 olderThan 且非 DOWN 的节点置为 DOWN，返回受影响数。
	MarkStaleDown(ctx context.Context, olderThan time.Time) (int64, error)
	// Close 释放底层资源。
	Close() error
}

// NewID 生成一个 16 位十六进制的随机 id。
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
