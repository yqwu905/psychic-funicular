// Package notify 实现事件引擎、规则路由与可插拔通知器。
//
// 框架只定义 Notifier 接口与「路由/去重/冷却/记录」机制，不内置任何真实渠道；
// 具体渠道(飞书/钉钉/邮件/IM…)由使用方实现接口并注册——内部实现通常很简单。
// 内置的 log 通知器只是默认 sink，便于观测与自检，并非真实渠道。
package notify

import (
	"context"
	"log/slog"
	"strings"

	"github.com/yqwu905/psychic-funicular/internal/event"
)

// Message 是交给通知器投递的渲染结果。
type Message struct {
	Event      event.Event
	Recipients []string
	Title      string
	Body       string
}

// Notifier 是通知通道接口。使用方实现它来接入真实渠道。
type Notifier interface {
	Name() string
	Notify(ctx context.Context, msg Message) error
}

// logNotifier 是内置默认 sink：把通知写进结构化日志。
type logNotifier struct{ log *slog.Logger }

func (logNotifier) Name() string { return "log" }

func (n logNotifier) Notify(_ context.Context, m Message) error {
	n.log.Info("NOTIFY",
		"type", m.Event.Type,
		"severity", string(m.Event.Severity),
		"to", strings.Join(m.Recipients, ","),
		"summary", m.Event.Summary)
	return nil
}
