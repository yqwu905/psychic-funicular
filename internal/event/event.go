// Package event 定义事件与通知的数据模型（被 store 持久化、被 notify 引擎消费）。
package event

import "time"

// Severity 是事件严重级别。
type Severity string

const (
	SevInfo     Severity = "info"
	SevWarning  Severity = "warning"
	SevCritical Severity = "critical"
)

// 事件类型常量。
const (
	TypeDiskFull      = "disk.full"
	TypeDiskRecovered = "disk.recovered"
	TypeDeviceIdle    = "device.idle"
	TypeJobCompleted  = "job.completed"
	TypeJobFailed     = "job.failed"
	TypeNodeDown      = "node.down"
	TypeNodeDiagnosed = "node.diagnosed"
)

// Event 是一次事件。
type Event struct {
	ID       string
	Type     string
	Severity Severity
	Source   string // 节点名 / 作业 id / system
	Summary  string
	DedupKey string // 同一逻辑事件的稳定键，用于去重/冷却与恢复配对
	Labels   map[string]string
	Time     time.Time
}

// Notification 是一条投递记录。
type Notification struct {
	ID         string
	EventID    string
	EventType  string
	Rule       string
	Channel    string
	Recipients string // 逗号分隔
	Status     string // sent | failed
	Error      string
	Summary    string
	Time       time.Time
}
