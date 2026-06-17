package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/event"
	"github.com/yqwu905/psychic-funicular/internal/notify"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"github.com/yqwu905/psychic-funicular/internal/transport"
)

// diagnoser 在「经 SSH 纳管的节点」失联够久后做基本诊断（SSH 连接中断 / agent 进程被杀 /
// 其他原因），把结论作为 node.diagnosed 事件上报；可选地在进程被杀时自动重新拉起 agent。
type diagnoser struct {
	store  store.Store
	events *notify.Engine
	log    *slog.Logger
	cfg    config.DiagnosticsConfig
	nodes  map[string]config.SSHNodeConfig // 按节点名索引的 SSH 纳管配置

	mu      sync.Mutex
	lastRun map[string]time.Time // 节点名 -> 上次诊断时间（冷却）

	// trigger 实际发起一次诊断；默认异步执行 run（SSH 拨号不阻塞巡检循环），测试可替换。
	trigger func(ctx context.Context, sn config.SSHNodeConfig, downFor time.Duration)
}

func newDiagnoser(st store.Store, events *notify.Engine, cfg config.ServerConfig, log *slog.Logger) *diagnoser {
	nodes := make(map[string]config.SSHNodeConfig, len(cfg.SSHNodes))
	for _, sn := range cfg.SSHNodes {
		nodes[sn.Name] = sn
	}
	d := &diagnoser{
		store: st, events: events, log: log, cfg: cfg.Diagnostics, nodes: nodes,
		lastRun: make(map[string]time.Time),
	}
	d.trigger = func(ctx context.Context, sn config.SSHNodeConfig, downFor time.Duration) {
		go d.run(ctx, sn, downFor)
	}
	return d
}

// check 在失联巡检的每个周期被调用：对失联够久的 SSH 纳管节点触发一次诊断（带冷却）。
// 冷却(默认 2m)远大于单次诊断耗时(SSH 超时约 10s)，故无需额外的并发保护。
func (d *diagnoser) check(ctx context.Context, nodes []*store.Node, now time.Time) {
	after := d.cfg.AfterDown.Std()
	cooldown := d.cfg.Cooldown.Std()
	for _, n := range nodes {
		sn, ok := d.nodes[n.Name]
		if !ok || n.State == store.StateDrain || n.LastHeartbeat.IsZero() {
			continue // 非 SSH 纳管 / 主动 drain / 从未上报，跳过
		}
		downFor := now.Sub(n.LastHeartbeat)

		d.mu.Lock()
		if downFor < after {
			// 心跳正常或尚未达阈值：清除冷却记录，便于下次失联即时诊断。
			delete(d.lastRun, n.Name)
			d.mu.Unlock()
			continue
		}
		if last, ok := d.lastRun[n.Name]; ok && now.Sub(last) < cooldown {
			d.mu.Unlock()
			continue // 冷却期内，跳过
		}
		d.lastRun[n.Name] = now
		d.mu.Unlock()

		d.trigger(ctx, sn, downFor)
	}
}

// run 执行单个节点的诊断并发事件；命中「进程被杀」且开启自愈时自动重新分发并拉起 agent。
func (d *diagnoser) run(ctx context.Context, sn config.SSHNodeConfig, downFor time.Duration) {
	spec := provisionSpec(sn)
	diag := transport.Diagnose(ctx, sshConfigFor(sn), spec.PidPath, spec.RemotePath, d.log)
	downText := downFor.Round(time.Second)
	d.log.Warn("node failure diagnosed", "node", sn.Name,
		"down_for", downText.String(), "cause", string(diag.Cause), "detail", diag.Detail)

	sev := event.SevWarning
	if diag.Cause == transport.DiagSSHDown {
		sev = event.SevCritical
	}
	d.events.Emit(ctx, event.Event{
		Type:     event.TypeNodeDiagnosed,
		Severity: sev,
		Source:   sn.Name,
		Summary: fmt.Sprintf("节点 %s 失联 %s，诊断结论：%s（%s）",
			sn.Name, downText, causeText(diag.Cause), diag.Detail),
		DedupKey: "node-diagnosed|" + sn.Name + "|" + string(diag.Cause),
		Labels:   map[string]string{"node": sn.Name, "cause": string(diag.Cause)},
	})

	if diag.Cause == transport.DiagAgentKilled && sn.Provision && sn.AutoRestart {
		d.log.Info("auto-restarting agent after diagnosis", "node", sn.Name)
		if err := transport.Provision(ctx, sshConfigFor(sn), spec, d.log); err != nil {
			d.log.Warn("auto-restart failed", "node", sn.Name, "err", err)
		} else {
			d.log.Info("agent re-provisioned", "node", sn.Name)
		}
	}
}

// causeText 把诊断结论转成中文摘要，用于事件 Summary。
func causeText(c transport.DiagCause) string {
	switch c {
	case transport.DiagSSHDown:
		return "SSH 连接中断"
	case transport.DiagAgentKilled:
		return "agent 进程被杀"
	default:
		return "其他原因"
	}
}
