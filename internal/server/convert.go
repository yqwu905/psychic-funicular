package server

import (
	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

// resourcesFromProto 把 proto Resources 拆进 store.Node 的资源字段。
func resourcesFromProto(n *store.Node, r *skipperv1.Resources) {
	if r == nil {
		return
	}
	n.CPUs = r.GetCpus()
	n.MemTotalBytes = r.GetMemTotalBytes()
	n.Devices = n.Devices[:0]
	for _, d := range r.GetDevices() {
		n.Devices = append(n.Devices, store.Device{
			Kind:          d.GetKind(),
			Vendor:        d.GetVendor(),
			Index:         d.GetIndex(),
			UUID:          d.GetUuid(),
			MemTotalBytes: d.GetMemTotalBytes(),
			Name:          d.GetName(),
		})
	}
}

// nodeFromSnapshot 从指标快照提取静态库存(核数/内存/设备清单)到 store.Node。
func nodeFromSnapshot(n *store.Node, snap *skipperv1.MetricsSnapshot) {
	if snap == nil {
		return
	}
	if snap.GetCpu() != nil {
		n.CPUs = snap.GetCpu().GetCores()
	}
	if snap.GetMem() != nil {
		n.MemTotalBytes = snap.GetMem().GetTotalBytes()
	}
	for _, d := range snap.GetDevices() {
		n.Devices = append(n.Devices, store.Device{
			Kind:          d.GetKind(),
			Vendor:        d.GetVendor(),
			Index:         d.GetIndex(),
			UUID:          d.GetUuid(),
			MemTotalBytes: d.GetMemTotalBytes(),
			Name:          d.GetName(),
		})
	}
}

// nodeToProto 把 store.Node 转为对外的 proto Node。
func nodeToProto(n *store.Node) *skipperv1.Node {
	devices := make([]*skipperv1.Device, 0, len(n.Devices))
	for _, d := range n.Devices {
		devices = append(devices, &skipperv1.Device{
			Kind:          d.Kind,
			Vendor:        d.Vendor,
			Index:         d.Index,
			Uuid:          d.UUID,
			MemTotalBytes: d.MemTotalBytes,
			Name:          d.Name,
		})
	}
	var hb int64
	if !n.LastHeartbeat.IsZero() {
		hb = n.LastHeartbeat.Unix()
	}
	return &skipperv1.Node{
		Id:        n.ID,
		Name:      n.Name,
		Partition: n.Partition,
		State:     n.State,
		Addr:      n.Addr,
		Resources: &skipperv1.Resources{
			Cpus:          n.CPUs,
			MemTotalBytes: n.MemTotalBytes,
			Devices:       devices,
		},
		Labels:            n.Labels,
		LastHeartbeatUnix: hb,
		AgentVersion:      n.AgentVersion,
	}
}
