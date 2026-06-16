package notify

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/config"
	"github.com/yqwu905/psychic-funicular/internal/event"
	logpkg "github.com/yqwu905/psychic-funicular/internal/log"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

type capture struct {
	count int
	last  Message
}

func (c *capture) Name() string { return "test" }
func (c *capture) Notify(_ context.Context, m Message) error {
	c.count++
	c.last = m
	return nil
}

func TestMatchesAndRecipients(t *testing.T) {
	r := config.NotifyRule{
		Match: config.RuleMatch{Type: config.StringOrSlice{"job.completed"}, Labels: map[string]string{"x": "1"}},
		To:    []string{"admins", "${owner}", "${missing}"},
	}
	ev := event.Event{Type: "job.completed", Labels: map[string]string{"x": "1", "owner": "alice"}}
	if !matches(r, ev) {
		t.Fatal("should match")
	}
	bad := ev
	bad.Type = "job.failed"
	if matches(r, bad) {
		t.Fatal("type mismatch should not match")
	}
	got := resolveRecipients(r.To, ev)
	if len(got) != 2 || got[0] != "admins" || got[1] != "alice" {
		t.Fatalf("recipients = %v, want [admins alice]", got)
	}
}

func TestEngineCooldownAndPersist(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.NotifyConfig{
		Channels: []string{"test"},
		Rules: []config.NotifyRule{{
			Name:     "r",
			Match:    config.RuleMatch{Type: config.StringOrSlice{"disk.full"}},
			To:       []string{"admins"},
			Channels: []string{"test"},
			Cooldown: config.Duration(time.Hour),
		}},
	}
	eng := New(cfg, st, logpkg.New("error"))
	cap := &capture{}
	eng.Register(cap)

	ctx := context.Background()
	ev := event.Event{Type: "disk.full", DedupKey: "node|/data", Summary: "full"}
	eng.Emit(ctx, ev)
	eng.Emit(ctx, ev) // 同去重键且在冷却内 -> 抑制
	if cap.count != 1 {
		t.Fatalf("want 1 delivery (cooldown), got %d", cap.count)
	}
	other := ev
	other.DedupKey = "node|/other"
	eng.Emit(ctx, other) // 不同去重键 -> 投递
	if cap.count != 2 {
		t.Fatalf("want 2 deliveries, got %d", cap.count)
	}

	evs, _ := st.ListEvents(ctx, 10)
	if len(evs) != 3 {
		t.Fatalf("want 3 events persisted, got %d", len(evs))
	}
	notes, _ := st.ListNotifications(ctx, 10)
	if len(notes) != 2 {
		t.Fatalf("want 2 notifications persisted, got %d", len(notes))
	}
}
