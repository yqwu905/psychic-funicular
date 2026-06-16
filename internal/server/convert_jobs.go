package server

import (
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func allocDevicesToProto(ds []store.AllocDevice) []*skipperv1.DeviceAssignment {
	out := make([]*skipperv1.DeviceAssignment, 0, len(ds))
	for _, d := range ds {
		out = append(out, &skipperv1.DeviceAssignment{Kind: d.Kind, Index: d.Index})
	}
	return out
}

func jobToProto(j *store.Job) *skipperv1.Job {
	return &skipperv1.Job{
		Id:        j.ID,
		Name:      j.Name,
		Owner:     j.Owner,
		Partition: j.Partition,
		State:     j.State,
		Priority:  j.Priority,
		Command:   j.Command,
		Env:       j.Env,
		Workdir:   j.Workdir,
		Request: &skipperv1.ResourceRequest{
			Cpus:        j.ReqCPUs,
			MemBytes:    j.ReqMemBytes,
			Gpus:        j.ReqGPUs,
			GpuType:     j.GPUType,
			WalltimeSec: j.WalltimeSec,
		},
		NodeId:       j.NodeID,
		NodeName:     j.NodeName,
		Devices:      allocDevicesToProto(j.Devices),
		ExitCode:     j.ExitCode,
		Reason:       j.Reason,
		SubmitAtUnix: unixOrZero(j.SubmitAt),
		StartAtUnix:  unixOrZero(j.StartAt),
		EndAtUnix:    unixOrZero(j.EndAt),
	}
}

func assignmentToProto(j *store.Job) *skipperv1.Assignment {
	return &skipperv1.Assignment{
		JobId:       j.ID,
		Command:     j.Command,
		Env:         j.Env,
		Workdir:     j.Workdir,
		Devices:     allocDevicesToProto(j.Devices),
		WalltimeSec: j.WalltimeSec,
	}
}
