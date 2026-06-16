package scheduler

import (
	"sort"
	"strings"
	"time"

	"github.com/yqwu905/psychic-funicular/internal/store"
)

// Policy 是调度策略参数。
type Policy struct {
	Backfill  bool    // 是否启用 EASY backfill（防止大作业被饿死 + 短作业填空隙）
	AgeWeight float64 // 排队每分钟增加的优先级（多因子老化）；0 关闭
}

// Decision 是一次分配：把作业放到某节点并占用一组设备。
type Decision struct {
	JobID    string
	NodeID   string
	NodeName string
	Devices  []store.AllocDevice
}

// Plan 计算一批分配决策（纯函数）。
//
// 排序：有效优先级(基础 + 老化)降序、提交时间升序。
// Backfill：遇到第一个放不下的作业时，按其在各节点上的最早可起始时间设一个「预留」，
// 之后只允许「能在预留时间前结束(有限 walltime)」的低优先级作业插队，从而既不饿死大作业、
// 又用短作业填满空隙。预留时间用当前运行作业的 start+walltime 估算，偏保守(绝不推迟被预留作业)。
func Plan(now time.Time, nodes []*store.Node, active, pending []*store.Job, policy Policy) []Decision {
	frees := buildFree(nodes, active)
	order := make([]*freeNode, 0, len(frees))
	for _, fn := range frees {
		order = append(order, fn)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].name < order[j].name })

	sorted := append([]*store.Job(nil), pending...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := effPriority(sorted[i], now, policy.AgeWeight), effPriority(sorted[j], now, policy.AgeWeight)
		if pi != pj {
			return pi > pj
		}
		return sorted[i].SubmitAt.Before(sorted[j].SubmitAt)
	})

	var (
		decisions []Decision
		resTime   time.Time
		haveRes   bool
	)
	for _, job := range sorted {
		fn, devs, ok := selectFit(job, order)
		if ok {
			if policy.Backfill && haveRes && !backfillAllowed(now, job, resTime) {
				continue // 会推迟被预留的作业，跳过
			}
			decisions = append(decisions, Decision{JobID: job.ID, NodeID: fn.id, NodeName: fn.name, Devices: devs})
			consume(fn, job, devs)
			continue
		}
		// 放不下：为最高优先级的不可调度作业设一次预留（EASY backfill，单预留）。
		if policy.Backfill && !haveRes {
			if t, found := reservationTime(now, job, nodes, active); found {
				resTime, haveRes = t, true
			}
		}
	}
	return decisions
}

// freeDevice / freeNode 是节点的可用资源视图。
type freeDevice struct {
	kind  string
	index uint32
	name  string
}

type freeNode struct {
	id, name, partition string
	cpus                uint32
	mem                 uint64
	devices             []freeDevice
}

func buildFree(nodes []*store.Node, active []*store.Job) map[string]*freeNode {
	frees := make(map[string]*freeNode, len(nodes))
	for _, n := range nodes {
		if n.State != store.StateUp {
			continue
		}
		devs := make([]freeDevice, 0, len(n.Devices))
		for _, d := range n.Devices {
			devs = append(devs, freeDevice{kind: d.Kind, index: d.Index, name: d.Name})
		}
		frees[n.ID] = &freeNode{id: n.ID, name: n.Name, partition: n.Partition,
			cpus: n.CPUs, mem: n.MemTotalBytes, devices: devs}
	}
	for _, job := range active {
		fn, ok := frees[job.NodeID]
		if !ok {
			continue
		}
		fn.cpus = subU32(fn.cpus, job.ReqCPUs)
		fn.mem = subU64(fn.mem, job.ReqMemBytes)
		fn.devices = removeFreeDevices(fn.devices, job.Devices)
	}
	return frees
}

func effPriority(j *store.Job, now time.Time, ageWeight float64) float64 {
	p := float64(j.Priority)
	if ageWeight > 0 && !j.SubmitAt.IsZero() {
		if wait := now.Sub(j.SubmitAt).Minutes(); wait > 0 {
			p += ageWeight * wait
		}
	}
	return p
}

// selectFit 在节点中找到能放下作业的，并返回分配到的设备。
func selectFit(job *store.Job, nodes []*freeNode) (*freeNode, []store.AllocDevice, bool) {
	for _, fn := range nodes {
		if job.Partition != "" && job.Partition != fn.partition {
			continue
		}
		if fn.cpus < job.ReqCPUs || fn.mem < job.ReqMemBytes {
			continue
		}
		picked := pickDevices(fn.devices, job.ReqGPUs, job.GPUType)
		if picked == nil {
			continue
		}
		return fn, picked, true
	}
	return nil, nil, false
}

// pickDevices 选出 n 块满足型号约束的设备；不足返回 nil。
func pickDevices(devs []freeDevice, n uint32, gpuType string) []store.AllocDevice {
	out := make([]store.AllocDevice, 0, n)
	if n == 0 {
		return out
	}
	for _, d := range devs {
		if !matchName(d.name, gpuType) {
			continue
		}
		out = append(out, store.AllocDevice{Kind: d.kind, Index: d.index})
		if uint32(len(out)) == n {
			return out
		}
	}
	return nil
}

func consume(fn *freeNode, job *store.Job, devs []store.AllocDevice) {
	fn.cpus = subU32(fn.cpus, job.ReqCPUs)
	fn.mem = subU64(fn.mem, job.ReqMemBytes)
	fn.devices = removeFreeDevices(fn.devices, devs)
}

type devKey struct {
	kind  string
	index uint32
}

func removeFreeDevices(list []freeDevice, used []store.AllocDevice) []freeDevice {
	if len(used) == 0 {
		return list
	}
	usedSet := make(map[devKey]struct{}, len(used))
	for _, u := range used {
		usedSet[devKey{u.Kind, u.Index}] = struct{}{}
	}
	out := list[:0:0]
	for _, d := range list {
		if _, ok := usedSet[devKey{d.kind, d.index}]; ok {
			continue
		}
		out = append(out, d)
	}
	return out
}

// matchName 判断设备型号是否满足 gpu_type 约束（空=不限；否则大小写不敏感子串）。
func matchName(name, gpuType string) bool {
	if gpuType == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(gpuType))
}

func backfillAllowed(now time.Time, job *store.Job, resTime time.Time) bool {
	if job.WalltimeSec <= 0 {
		return false // 无界作业可能无限期占用，不能保证在预留前结束
	}
	return !now.Add(time.Duration(job.WalltimeSec) * time.Second).After(resTime)
}

// reservationTime 估算 blocked 作业在各节点上的最早可起始时间，取最小者。
func reservationTime(now time.Time, blocked *store.Job, nodes []*store.Node, active []*store.Job) (time.Time, bool) {
	var best time.Time
	found := false
	for _, n := range nodes {
		if n.State != store.StateUp {
			continue
		}
		if blocked.Partition != "" && blocked.Partition != n.Partition {
			continue
		}
		// 节点必须在满容量下能放下该作业，否则无从预留。
		if n.CPUs < blocked.ReqCPUs || n.MemTotalBytes < blocked.ReqMemBytes ||
			uint32(countMatchingDevices(n.Devices, blocked.GPUType)) < blocked.ReqGPUs {
			continue
		}
		t, ok := nodeReservationTime(now, blocked, n, active)
		if !ok {
			continue
		}
		if !found || t.Before(best) {
			best, found = t, true
		}
	}
	return best, found
}

type occupation struct {
	end   time.Time
	cpus  uint32
	mem   uint64
	match int
}

func nodeReservationTime(now time.Time, blocked *store.Job, n *store.Node, active []*store.Job) (time.Time, bool) {
	var occs []occupation
	for _, j := range active {
		if j.NodeID != n.ID {
			continue
		}
		occs = append(occs, occupation{
			end:  estEnd(now, j),
			cpus: j.ReqCPUs, mem: j.ReqMemBytes,
			match: countJobMatching(j, n, blocked.GPUType),
		})
	}
	freeCPUs := n.CPUs
	freeMem := n.MemTotalBytes
	freeMatch := countMatchingDevices(n.Devices, blocked.GPUType)
	for _, o := range occs {
		freeCPUs = subU32(freeCPUs, o.cpus)
		freeMem = subU64(freeMem, o.mem)
		freeMatch -= o.match
	}
	fits := func() bool {
		return freeCPUs >= blocked.ReqCPUs && freeMem >= blocked.ReqMemBytes && freeMatch >= int(blocked.ReqGPUs)
	}
	if fits() {
		return now, true
	}
	sort.Slice(occs, func(i, j int) bool { return occs[i].end.Before(occs[j].end) })
	for _, o := range occs {
		freeCPUs += o.cpus
		freeMem += o.mem
		freeMatch += o.match
		if fits() {
			return o.end, true
		}
	}
	return time.Time{}, false
}

// estEnd 估算运行作业的结束时刻；无界(walltime<=0)记为远期。
func estEnd(now time.Time, j *store.Job) time.Time {
	if j.WalltimeSec <= 0 {
		return now.Add(100 * 365 * 24 * time.Hour)
	}
	base := j.StartAt
	if base.IsZero() {
		base = now
	}
	end := base.Add(time.Duration(j.WalltimeSec) * time.Second)
	if end.Before(now) {
		return now
	}
	return end
}

func countMatchingDevices(devs []store.Device, gpuType string) int {
	c := 0
	for _, d := range devs {
		if matchName(d.Name, gpuType) {
			c++
		}
	}
	return c
}

func countJobMatching(j *store.Job, n *store.Node, gpuType string) int {
	names := make(map[devKey]string, len(n.Devices))
	for _, d := range n.Devices {
		names[devKey{d.Kind, d.Index}] = d.Name
	}
	c := 0
	for _, d := range j.Devices {
		if matchName(names[devKey{d.Kind, d.Index}], gpuType) {
			c++
		}
	}
	return c
}

func subU32(a, b uint32) uint32 {
	if b > a {
		return 0
	}
	return a - b
}

func subU64(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}
