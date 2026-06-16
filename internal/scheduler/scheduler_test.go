package scheduler

import (
	"testing"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/store"
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
	t0 := time.Unix(1000, 0)
	nodes := []*store.Node{
		gpuNode("n1", "a-node", 8, 2),
		{ID: "n2", Name: "b-node", Partition: "cpu", State: store.StateUp, CPUs: 4, MemTotalBytes: 8 << 30},
	}
	pending := []*store.Job{
		job("j1", 10, "gpu", 2, 1, t0),
		job("j2", 5, "cpu", 2, 0, t0.Add(time.Second)),
		job("j3", 1, "gpu", 0, 2, t0.Add(2*time.Second)), // 需要 2 卡，但 j1 已占 1 卡 -> 放不下
	}

	ds := Plan(nodes, nil, pending)
	if len(ds) != 2 {
		t.Fatalf("want 2 decisions, got %d: %+v", len(ds), ds)
	}

	d1, ok := find(ds, "j1")
	if !ok || d1.NodeName != "a-node" || len(d1.Devices) != 1 || d1.Devices[0].Index != 0 {
		t.Fatalf("j1 misallocated: %+v", d1)
	}
	d2, ok := find(ds, "j2")
	if !ok || d2.NodeName != "b-node" {
		t.Fatalf("j2 should land on cpu node: %+v", d2)
	}
	if _, ok := find(ds, "j3"); ok {
		t.Fatal("j3 should not be scheduled (only 1 GPU left)")
	}
}

func TestPlanPriorityWins(t *testing.T) {
	t0 := time.Unix(1000, 0)
	nodes := []*store.Node{gpuNode("n1", "a-node", 8, 1)} // 仅 1 张卡
	// 低优先级先提交，高优先级后提交；高优先级应抢到唯一的卡。
	pending := []*store.Job{
		job("low", 1, "gpu", 1, 1, t0),
		job("high", 100, "gpu", 1, 1, t0.Add(time.Second)),
	}
	ds := Plan(nodes, nil, pending)
	if len(ds) != 1 {
		t.Fatalf("want 1 decision, got %d", len(ds))
	}
	if ds[0].JobID != "high" {
		t.Fatalf("higher priority should win, got %s", ds[0].JobID)
	}
}

func TestPlanRespectsActiveUsage(t *testing.T) {
	nodes := []*store.Node{gpuNode("n1", "a-node", 4, 2)}
	// 已有一个 RUNNING 作业占了 gpu0 + 3 核。
	active := []*store.Job{{ID: "r", State: store.JobRunning, NodeID: "n1",
		ReqCPUs: 3, ReqGPUs: 1, Devices: []store.AllocDevice{{Kind: "gpu", Index: 0}}}}
	pending := []*store.Job{job("j", 1, "gpu", 2, 1, time.Unix(1000, 0))} // 需要 2 核，只剩 1 核 -> 放不下
	ds := Plan(nodes, active, pending)
	if len(ds) != 0 {
		t.Fatalf("want 0 decisions (insufficient cpu), got %+v", ds)
	}
}
