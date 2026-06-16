package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteNodeLifecycle(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	// 首次注册。
	n := &Node{ID: NewID(), Name: "n1", Partition: "gpu", State: StateUp,
		CPUs: 8, MemTotalBytes: 1 << 34, LastHeartbeat: time.Now()}
	id1, err := st.RegisterNode(ctx, n)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id1 != n.ID {
		t.Fatalf("want id %s got %s", n.ID, id1)
	}

	// 同名再注册应保留原 id 并更新字段。
	id2, err := st.RegisterNode(ctx, &Node{ID: NewID(), Name: "n1", Partition: "cpu",
		State: StateUp, CPUs: 16, LastHeartbeat: time.Now()})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("re-register should keep id: want %s got %s", id1, id2)
	}

	nodes, err := st.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 node got %d", len(nodes))
	}
	if nodes[0].Partition != "cpu" || nodes[0].CPUs != 16 {
		t.Fatalf("upsert did not update fields: %+v", nodes[0])
	}

	// 已知节点心跳成功。
	found, err := st.Heartbeat(ctx, &Node{ID: id1, CPUs: 16}, time.Now())
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !found {
		t.Fatal("heartbeat should find node")
	}

	// 未知节点心跳返回 not found。
	found, err = st.Heartbeat(ctx, &Node{ID: "deadbeefdeadbeef"}, time.Now())
	if err != nil {
		t.Fatalf("heartbeat unknown: %v", err)
	}
	if found {
		t.Fatal("heartbeat should not find unknown node")
	}

	// 过期心跳 -> 巡检置 DOWN。
	if _, err := st.Heartbeat(ctx, &Node{ID: id1}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("stale heartbeat: %v", err)
	}
	affected, err := st.MarkStaleDown(ctx, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	if affected != 1 {
		t.Fatalf("want 1 marked down got %d", affected)
	}
	nodes, _ = st.ListNodes(ctx)
	if nodes[0].State != StateDown {
		t.Fatalf("want DOWN got %s", nodes[0].State)
	}
}
