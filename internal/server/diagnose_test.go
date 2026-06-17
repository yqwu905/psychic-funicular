package server

import (
	"context"
	"testing"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

// newTestDiagnoser 构造一个只用于测试 check() 决策逻辑的 diagnoser：
// 用 trigger 桩记录被触发诊断的节点，不做真正的 SSH I/O。
func newTestDiagnoser(after, cooldown time.Duration, names ...string) (*diagnoser, *[]string) {
	nodes := map[string]config.SSHNodeConfig{}
	for _, n := range names {
		nodes[n] = config.SSHNodeConfig{Name: n}
	}
	triggered := &[]string{}
	d := &diagnoser{
		cfg: config.DiagnosticsConfig{
			Enabled:   true,
			AfterDown: config.Duration(after),
			Cooldown:  config.Duration(cooldown),
		},
		nodes:   nodes,
		lastRun: map[string]time.Time{},
	}
	d.trigger = func(_ context.Context, sn config.SSHNodeConfig, _ time.Duration) {
		*triggered = append(*triggered, sn.Name)
	}
	return d, triggered
}

func node(name, state string, lastHB time.Time) *store.Node {
	return &store.Node{Name: name, State: state, LastHeartbeat: lastHB}
}

func TestDiagnoserCheckSelection(t *testing.T) {
	now := time.Now()
	after := time.Minute
	d, triggered := newTestDiagnoser(after, 2*time.Minute, "ssh-1", "ssh-2", "drain-1")

	nodes := []*store.Node{
		node("ssh-1", store.StateDown, now.Add(-90*time.Second)), // 失联够久 → 诊断
		node("ssh-2", store.StateUp, now.Add(-10*time.Second)),   // 心跳新 → 跳过
		node("drain-1", store.StateDrain, now.Add(-1*time.Hour)), // 主动 drain → 跳过
		node("direct-1", store.StateDown, now.Add(-1*time.Hour)), // 非 SSH 纳管 → 跳过
		node("ssh-zero", store.StateDown, time.Time{}),           // 从未上报 → 跳过
	}
	d.check(context.Background(), nodes, now)

	if got := *triggered; len(got) != 1 || got[0] != "ssh-1" {
		t.Fatalf("expected only ssh-1 diagnosed, got %v", got)
	}
}

func TestDiagnoserCooldown(t *testing.T) {
	now := time.Now()
	after := time.Minute
	cooldown := 2 * time.Minute
	d, triggered := newTestDiagnoser(after, cooldown, "ssh-1")
	down := func(at time.Time) []*store.Node {
		return []*store.Node{node("ssh-1", store.StateDown, at.Add(-90*time.Second))}
	}

	d.check(context.Background(), down(now), now)
	// 冷却期内再次巡检：不应重复诊断。
	d.check(context.Background(), down(now.Add(30*time.Second)), now.Add(30*time.Second))
	if got := *triggered; len(got) != 1 {
		t.Fatalf("expected 1 diagnosis within cooldown, got %d (%v)", len(got), got)
	}
	// 冷却结束后：再次诊断。
	later := now.Add(cooldown + time.Second)
	d.check(context.Background(), down(later), later)
	if got := *triggered; len(got) != 2 {
		t.Fatalf("expected 2 diagnoses after cooldown, got %d (%v)", len(got), got)
	}
}

func TestDiagnoserRecoveryResetsCooldown(t *testing.T) {
	now := time.Now()
	after := time.Minute
	d, triggered := newTestDiagnoser(after, 2*time.Minute, "ssh-1")

	// 失联 → 诊断一次。
	d.check(context.Background(), []*store.Node{node("ssh-1", store.StateDown, now.Add(-90*time.Second))}, now)

	// 节点恢复（心跳刷新）→ 清除冷却记录。
	recov := now.Add(5 * time.Second)
	d.check(context.Background(), []*store.Node{node("ssh-1", store.StateUp, recov)}, recov)

	// 再次失联：即便距上次诊断未满冷却，也应立即重新诊断。
	again := recov.Add(2 * time.Second)
	d.check(context.Background(), []*store.Node{node("ssh-1", store.StateDown, again.Add(-90*time.Second))}, again)

	if got := *triggered; len(got) != 2 {
		t.Fatalf("expected re-diagnosis after recovery, got %d (%v)", len(got), got)
	}
}
