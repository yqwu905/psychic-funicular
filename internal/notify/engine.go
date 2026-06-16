package notify

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/event"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

// Engine 是事件引擎：把事件按规则路由到通知器，并做去重/冷却与记录。
type Engine struct {
	cfg   config.NotifyConfig
	rules []config.NotifyRule
	store store.Store
	log   *slog.Logger

	mu        sync.Mutex
	notifiers map[string]Notifier
	cooldown  map[string]time.Time
}

// New 创建引擎；配置未提供规则时使用内置默认规则。内置 log 通知器自动注册。
func New(cfg config.NotifyConfig, st store.Store, log *slog.Logger) *Engine {
	rules := cfg.Rules
	if len(rules) == 0 {
		rules = DefaultRules()
	}
	e := &Engine{
		cfg: cfg, rules: rules, store: st, log: log,
		notifiers: make(map[string]Notifier),
		cooldown:  make(map[string]time.Time),
	}
	e.Register(logNotifier{log: log})
	return e
}

// Register 注册一个通知器（使用方接入真实渠道时调用）。
func (e *Engine) Register(n Notifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifiers[n.Name()] = n
}

// Emit 处理一个事件：落库 → 规则匹配 → 去重/冷却 → 通道投递 → 记录。
func (e *Engine) Emit(ctx context.Context, ev event.Event) {
	if ev.ID == "" {
		ev.ID = store.NewID()
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	if err := e.store.CreateEvent(ctx, &ev); err != nil {
		e.log.Error("persist event failed", "err", err)
	}
	for _, r := range e.rules {
		if !matches(r, ev) || !e.allow(r, ev) {
			continue
		}
		recipients := resolveRecipients(r.To, ev)
		channels := r.Channels
		if len(channels) == 0 {
			channels = e.cfg.Channels
		}
		if len(channels) == 0 {
			channels = []string{"log"}
		}
		for _, ch := range channels {
			e.deliver(ctx, r.Name, ev, ch, recipients)
		}
	}
}

// allow 检查并更新冷却：同一(规则,去重键)在冷却时长内只放行一次。
func (e *Engine) allow(r config.NotifyRule, ev event.Event) bool {
	cd := r.Cooldown.Std()
	if cd <= 0 {
		return true
	}
	key := r.Name + "|" + ev.DedupKey
	e.mu.Lock()
	defer e.mu.Unlock()
	if last, ok := e.cooldown[key]; ok && time.Since(last) < cd {
		return false
	}
	e.cooldown[key] = time.Now()
	return true
}

func (e *Engine) deliver(ctx context.Context, rule string, ev event.Event, channel string, recipients []string) {
	e.mu.Lock()
	n := e.notifiers[channel]
	e.mu.Unlock()

	note := &event.Notification{
		ID: store.NewID(), EventID: ev.ID, EventType: ev.Type, Rule: rule,
		Channel: channel, Recipients: strings.Join(recipients, ","),
		Summary: ev.Summary, Status: "sent", Time: time.Now(),
	}
	switch {
	case n == nil:
		note.Status, note.Error = "failed", "no such channel"
		e.log.Warn("notify channel not found", "channel", channel, "rule", rule)
	default:
		msg := Message{Event: ev, Recipients: recipients, Title: ev.Type, Body: ev.Summary}
		if err := n.Notify(ctx, msg); err != nil {
			note.Status, note.Error = "failed", err.Error()
			e.log.Warn("notify failed", "channel", channel, "rule", rule, "err", err)
		}
	}
	if err := e.store.CreateNotification(ctx, note); err != nil {
		e.log.Error("persist notification failed", "err", err)
	}
}
