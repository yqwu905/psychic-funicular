package scheduler

import (
	"testing"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/store"
)

var (
	testNow   = time.Unix(100000, 0)
	defPolicy = Policy{Backfill: true}
)

func gpuNode(id, name string, cpus uint32, gpus int) *store.Node {
	devs := make([]store.Device, gpus)
	for i := 0; i < gpus; i++ {
		devs[i] = store.Device{Kind: "gpu", Vendor: "nvidia", Index: uint32(i)}
	}
	return &store.Node{ID: id, Name: name, Partition: "gpu", State: store.StateUp,
		CPUs: cpus, MemTotalBytes: 64 << 30, Devices: devs}
}

func job(id string, prio int32, part string, cpus, gpus uint32, submit time.Time) *store.Job {
	return &store.Job{ID: id, State: store.JobPending, Priority: prio, Partition: part,
		ReqCPUs: cpus, ReqGPUs: gpus, SubmitAt: submit}
}

func find(ds []Decision, jobID string) (Decision, bool) {
	for _, d := range ds {
		if d.JobID == jobID {
			return d, true
		}
	}
	return Decision{}, false
}

func TestPlanFitAndPriority(t *testing.T) {
	t0 := testNow
	nodes := []*store.Node{
		gpuNode("n1", "a-node", 8, 2),
		{ID: "n2", Name: "b-node", Partition: "cpu", State: store.StateUp, CPUs: 4, MemTotalBytes: 8 << 30},
	}
	pending := []*store.Job{
		job("j1", 10, "gpu", 2, 1, t0),
		job("j2", 5, "cpu", 2, 0, t0.Add(time.Second)),
		job("j3", 1, "gpu", 0, 2, t0.Add(2*time.Second)), // 需要 2 卡，但 j1 已占 1 卡 -> 放不下
	}

	ds := Plan(testNow, nodes, nil, pending, defPolicy)
	if len(ds) != 2 {
		t.Fatalf("want 2 decisions, got %d: %+v", len(ds), ds)
	}
	d1, ok := find(ds, "j1")
	if !ok || d1.NodeName != "a-node" || len(d1.Devices) != 1 || d1.Devices[0].Index != 0 {
		t.Fatalf("j1 misallocated: %+v", d1)
	}
	if d2, ok := find(ds, "j2"); !ok || d2.NodeName != "b-node" {
		t.Fatalf("j2 should land on cpu node: %+v", d2)
	}
	if _, ok := find(ds, "j3"); ok {
		t.Fatal("j3 should not be scheduled (only 1 GPU left)")
	}
}

func TestPlanPriorityWins(t *testing.T) {
	nodes := []*store.Node{gpuNode("n1", "a-node", 8, 1)}
	pending := []*store.Job{
		job("low", 1, "gpu", 1, 1, testNow),
		job("high", 100, "gpu", 1, 1, testNow.Add(time.Second)),
	}
	ds := Plan(testNow, nodes, nil, pending, defPolicy)
	if len(ds) != 1 || ds[0].JobID != "high" {
		t.Fatalf("higher priority should win, got %+v", ds)
	}
}

func TestPlanRespectsActiveUsage(t *testing.T) {
	nodes := []*store.Node{gpuNode("n1", "a-node", 4, 2)}
	active := []*store.Job{{ID: "r", State: store.JobRunning, NodeID: "n1",
		ReqCPUs: 3, ReqGPUs: 1, Devices: []store.AllocDevice{{Kind: "gpu", Index: 0}}}}
	pending := []*store.Job{job("j", 1, "gpu", 2, 1, testNow)} // 需要 2 核，只剩 1 核
	ds := Plan(testNow, nodes, active, pending, defPolicy)
	if len(ds) != 0 {
		t.Fatalf("want 0 decisions (insufficient cpu), got %+v", ds)
	}
}

func TestPlanGPUType(t *testing.T) {
	node := &store.Node{ID: "n1", Name: "n1", Partition: "gpu", State: store.StateUp,
		CPUs: 8, MemTotalBytes: 64 << 30, Devices: []store.Device{
			{Kind: "gpu", Index: 0, Name: "NVIDIA RTX 3090"},
			{Kind: "gpu", Index: 1, Name: "NVIDIA A100-SXM4-40GB"},
		}}
	// 要 1 张 A100：必须挑到 index 1，而非 3090。
	j := &store.Job{ID: "j", State: store.JobPending, Partition: "gpu", ReqGPUs: 1,
		GPUType: "A100", SubmitAt: testNow}
	ds := Plan(testNow, []*store.Node{node}, nil, []*store.Job{j}, Policy{})
	if len(ds) != 1 || len(ds[0].Devices) != 1 || ds[0].Devices[0].Index != 1 {
		t.Fatalf("should pick the A100 (index 1): %+v", ds)
	}
	// 要 2 张 A100：只有 1 张 -> 放不下。
	j2 := &store.Job{ID: "j2", State: store.JobPending, Partition: "gpu", ReqGPUs: 2,
		GPUType: "A100", SubmitAt: testNow}
	if ds := Plan(testNow, []*store.Node{node}, nil, []*store.Job{j2}, Policy{}); len(ds) != 0 {
		t.Fatalf("want 0 (only one A100), got %+v", ds)
	}
}

func TestPlanBackfill(t *testing.T) {
	nodes := []*store.Node{gpuNode("n1", "n1", 4, 1)}
	// 运行中作业占住唯一的 GPU，预计 10 分钟后结束。
	active := []*store.Job{{ID: "run", State: store.JobRunning, NodeID: "n1",
		ReqCPUs: 1, ReqGPUs: 1, WalltimeSec: 600, StartAt: testNow,
		Devices: []store.AllocDevice{{Kind: "gpu", Index: 0}}}}
	big := &store.Job{ID: "big", State: store.JobPending, Priority: 100, Partition: "gpu",
		ReqCPUs: 1, ReqGPUs: 1, SubmitAt: testNow} // 需要 GPU，被阻塞
	short := &store.Job{ID: "short", State: store.JobPending, Priority: 1, Partition: "gpu",
		ReqCPUs: 1, WalltimeSec: 60, SubmitAt: testNow} // 1 分钟，能在预留前结束 -> 可回填
	long := &store.Job{ID: "long", State: store.JobPending, Priority: 1, Partition: "gpu",
		ReqCPUs: 1, WalltimeSec: 1800, SubmitAt: testNow.Add(time.Second)} // 30 分钟 -> 不可回填

	ds := Plan(testNow, nodes, active, []*store.Job{big, short, long}, Policy{Backfill: true})
	if _, ok := find(ds, "big"); ok {
		t.Fatal("big can't fit, should not be scheduled")
	}
	if _, ok := find(ds, "short"); !ok {
		t.Fatal("short should backfill (finishes before reservation)")
	}
	if _, ok := find(ds, "long"); ok {
		t.Fatal("long should NOT backfill (would delay the reserved big job)")
	}

	// 关闭 backfill：贪心会让 long 也插队（从而可能饿死 big）。
	greedy := Plan(testNow, nodes, active, []*store.Job{big, short, long}, Policy{Backfill: false})
	if _, ok := find(greedy, "long"); !ok {
		t.Fatal("without backfill, greedy should schedule long too")
	}
}

func TestPlanAging(t *testing.T) {
	nodes := []*store.Node{{ID: "n1", Name: "n1", State: store.StateUp, CPUs: 1, MemTotalBytes: 8 << 30}}
	old := job("old", 1, "", 1, 0, testNow.Add(-100*time.Minute))
	fresh := job("new", 5, "", 1, 0, testNow)

	// 无老化：高优先级的 new 胜出。
	ds := Plan(testNow, nodes, nil, []*store.Job{old, fresh}, Policy{})
	if len(ds) != 1 || ds[0].JobID != "new" {
		t.Fatalf("without aging, new should win: %+v", ds)
	}
	// 有老化：old 等待 100 分钟 * 0.1 = +10 优先级 -> 反超 new。
	ds = Plan(testNow, nodes, nil, []*store.Job{old, fresh}, Policy{AgeWeight: 0.1})
	if len(ds) != 1 || ds[0].JobID != "old" {
		t.Fatalf("with aging, old should overtake: %+v", ds)
	}
}
