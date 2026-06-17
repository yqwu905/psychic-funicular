package notify

import (
	"strings"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/event"
)

var severityRank = map[event.Severity]int{
	event.SevInfo:     0,
	event.SevWarning:  1,
	event.SevCritical: 2,
}

// matches 判断事件是否命中规则（类型、标签子集、最低严重级别）。
func matches(r config.NotifyRule, ev event.Event) bool {
	if len(r.Match.Type) > 0 && !contains(r.Match.Type, ev.Type) {
		return false
	}
	for k, v := range r.Match.Labels {
		if ev.Labels[k] != v {
			return false
		}
	}
	if r.Match.MinSeverity != "" {
		if severityRank[ev.Severity] < severityRank[event.Severity(r.Match.MinSeverity)] {
			return false
		}
	}
	return true
}

func contains(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

// resolveRecipients 解析接收人：`${label}` 取事件标签值，否则按字面量；去重并丢弃空值。
func resolveRecipients(to []string, ev event.Event) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, t := range to {
		v := t
		if strings.HasPrefix(t, "${") && strings.HasSuffix(t, "}") {
			v = ev.Labels[t[2:len(t)-1]]
		}
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// DefaultRules 是配置未提供规则时使用的内置默认规则。
func DefaultRules() []config.NotifyRule {
	return []config.NotifyRule{
		{Name: "disk-full", Match: config.RuleMatch{Type: []string{event.TypeDiskFull}},
			To: []string{"admins"}, Cooldown: config.Duration(time.Hour)},
		{Name: "job-finished", Match: config.RuleMatch{Type: []string{event.TypeJobCompleted, event.TypeJobFailed}},
			To: []string{"${owner}"}},
		{Name: "device-idle-allocated", Match: config.RuleMatch{Type: []string{event.TypeDeviceIdle},
			Labels: map[string]string{"allocated": "true"}},
			To: []string{"${owner}"}, Cooldown: config.Duration(2 * time.Hour)},
		{Name: "device-idle-free", Match: config.RuleMatch{Type: []string{event.TypeDeviceIdle},
			Labels: map[string]string{"allocated": "false"}},
			To: []string{"admins"}, Cooldown: config.Duration(2 * time.Hour)},
		{Name: "node-down", Match: config.RuleMatch{Type: []string{event.TypeNodeDown}},
			To: []string{"admins"}, Cooldown: config.Duration(10 * time.Minute)},
		{Name: "node-diagnosed", Match: config.RuleMatch{Type: []string{event.TypeNodeDiagnosed}},
			To: []string{"admins"}, Cooldown: config.Duration(10 * time.Minute)},
	}
}
