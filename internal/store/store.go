// Package store 定义持久化模型与接口。M0 仅含节点；作业/事件等后续里程碑加入。
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/event"
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
	// GetNode 按 id 返回节点；不存在返回 (nil, nil)。
	GetNode(ctx context.Context, id string) (*Node, error)
	// MarkStaleDown 把 last_heartbeat 早于 olderThan 且非 DOWN 的节点置为 DOWN，返回受影响数。
	MarkStaleDown(ctx context.Context, olderThan time.Time) (int64, error)

	// --- 作业 ---

	// CreateJob 落库一个新作业（通常为 PENDING）。
	CreateJob(ctx context.Context, j *Job) error
	// GetJob 按 id 返回作业；不存在返回 (nil, nil)。
	GetJob(ctx context.Context, id string) (*Job, error)
	// ListJobs 返回作业，可按状态/属主过滤(空串表示不过滤)，按提交时间倒序。
	ListJobs(ctx context.Context, state, owner string) ([]*Job, error)
	// ListPendingJobs 返回待调度作业，按优先级降序、提交时间升序。
	ListPendingJobs(ctx context.Context) ([]*Job, error)
	// ListActiveJobs 返回占用资源的作业(ASSIGNED/RUNNING)，用于调度核算。
	ListActiveJobs(ctx context.Context) ([]*Job, error)
	// ListJobsByNodeState 返回某节点指定状态的作业。
	ListJobsByNodeState(ctx context.Context, nodeID, state string) ([]*Job, error)
	// AssignJob 把 PENDING 作业分配到节点(置 ASSIGNED)；仅当前状态为 PENDING 时生效。
	AssignJob(ctx context.Context, jobID, nodeID, nodeName string, devices []AllocDevice) error
	// MarkJobRunning 把作业置 RUNNING 并记录开始时间。
	MarkJobRunning(ctx context.Context, jobID string, t time.Time) error
	// FinishJob 把作业置终态并记录退出码/原因/结束时间。
	FinishJob(ctx context.Context, jobID, state string, exitCode int32, reason string, t time.Time) error
	// CancelJob 取消作业：PENDING 直接置 CANCELLED；ASSIGNED/RUNNING 置 CANCELLING。返回新状态。
	CancelJob(ctx context.Context, jobID string) (newState string, err error)

	// --- 事件与通知 ---

	// CreateEvent 落库一个事件。
	CreateEvent(ctx context.Context, e *event.Event) error
	// ListEvents 返回最近的事件(按时间倒序，最多 limit 条)。
	ListEvents(ctx context.Context, limit int) ([]*event.Event, error)
	// CreateNotification 落库一条通知投递记录。
	CreateNotification(ctx context.Context, n *event.Notification) error
	// ListNotifications 返回最近的通知记录(按时间倒序，最多 limit 条)。
	ListNotifications(ctx context.Context, limit int) ([]*event.Notification, error)

	// Close 释放底层资源。
	Close() error
}

// 作业状态常量。
const (
	JobPending    = "PENDING"
	JobAssigned   = "ASSIGNED"
	JobRunning    = "RUNNING"
	JobCompleted  = "COMPLETED"
	JobFailed     = "FAILED"
	JobCancelling = "CANCELLING"
	JobCancelled  = "CANCELLED"
	JobTimeout    = "TIMEOUT"
)

// AllocDevice 是分配给作业的一块设备。
type AllocDevice struct {
	Kind  string `json:"kind"`
	Index uint32 `json:"index"`
}

// Job 是一个作业的持久化视图。
type Job struct {
	ID          string
	Name        string
	Owner       string
	Partition   string
	State       string
	Priority    int32
	Command     string
	Env         map[string]string
	Workdir     string
	ReqCPUs     uint32
	ReqMemBytes uint64
	ReqGPUs     uint32
	GPUType     string
	WalltimeSec int64
	NodeID      string
	NodeName    string
	Devices     []AllocDevice
	ExitCode    int32
	Reason      string
	SubmitAt    time.Time
	StartAt     time.Time
	EndAt       time.Time
}

// NewID 生成一个 16 位十六进制的随机 id。
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
