package server

import (
	"context"
	"io"
	"os"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *clusterService) SubmitJob(ctx context.Context, req *skipperv1.SubmitJobRequest) (*skipperv1.SubmitJobResponse, error) {
	if req.GetCommand() == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}
	r := req.GetRequest()
	j := &store.Job{
		ID:          store.NewID(),
		Name:        req.GetName(),
		Owner:       ownerOrDefault(req.GetOwner()),
		Partition:   req.GetPartition(),
		State:       store.JobPending,
		Priority:    req.GetPriority(),
		Command:     req.GetCommand(),
		Env:         req.GetEnv(),
		Workdir:     req.GetWorkdir(),
		ReqCPUs:     r.GetCpus(),
		ReqMemBytes: r.GetMemBytes(),
		ReqGPUs:     r.GetGpus(),
		GPUType:     r.GetGpuType(),
		WalltimeSec: r.GetWalltimeSec(),
		SubmitAt:    time.Now(),
	}
	if err := s.store.CreateJob(ctx, j); err != nil {
		s.log.Error("submit job failed", "err", err)
		return nil, status.Error(codes.Internal, "submit job failed")
	}
	s.log.Info("job submitted", "job", j.ID, "name", j.Name, "owner", j.Owner,
		"partition", j.Partition, "cpus", j.ReqCPUs, "gpus", j.ReqGPUs)
	return &skipperv1.SubmitJobResponse{JobId: j.ID}, nil
}

func (s *clusterService) ListJobs(ctx context.Context, req *skipperv1.ListJobsRequest) (*skipperv1.ListJobsResponse, error) {
	jobs, err := s.store.ListJobs(ctx, req.GetState(), req.GetOwner())
	if err != nil {
		return nil, status.Error(codes.Internal, "list jobs failed")
	}
	resp := &skipperv1.ListJobsResponse{Jobs: make([]*skipperv1.Job, 0, len(jobs))}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, jobToProto(j))
	}
	return resp, nil
}

func (s *clusterService) GetJob(ctx context.Context, req *skipperv1.GetJobRequest) (*skipperv1.Job, error) {
	j, err := s.store.GetJob(ctx, req.GetJobId())
	if err != nil {
		return nil, status.Error(codes.Internal, "get job failed")
	}
	if j == nil {
		return nil, status.Error(codes.NotFound, "job not found")
	}
	return jobToProto(j), nil
}

func (s *clusterService) CancelJob(ctx context.Context, req *skipperv1.CancelJobRequest) (*skipperv1.CancelJobResponse, error) {
	if req.GetJobId() == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id is required")
	}
	state, err := s.store.CancelJob(ctx, req.GetJobId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	s.log.Info("job cancel requested", "job", req.GetJobId(), "new_state", state)
	return &skipperv1.CancelJobResponse{Ok: true, State: state}, nil
}

func (s *clusterService) GetJobLogs(req *skipperv1.GetJobLogsRequest, stream grpc.ServerStreamingServer[skipperv1.LogChunk]) error {
	if req.GetJobId() == "" {
		return status.Error(codes.InvalidArgument, "job_id is required")
	}
	ctx := stream.Context()
	job, err := s.store.GetJob(ctx, req.GetJobId())
	if err != nil {
		return status.Error(codes.Internal, "get job failed")
	}
	if job == nil {
		return status.Error(codes.NotFound, "job not found")
	}

	var f *os.File
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		if f == nil {
			file, err := s.jobLogs.open(req.GetJobId())
			switch {
			case err == nil:
				f = file
			case !os.IsNotExist(err):
				return status.Error(codes.Internal, "open logs failed")
			}
		}
		if f != nil {
			if err := drainLogs(f, buf, stream); err != nil {
				return err
			}
		}
		if !req.GetFollow() {
			return nil
		}
		// 终态后再 drain 一次以确保收尾日志送达，然后结束。
		if j, err := s.store.GetJob(ctx, req.GetJobId()); err == nil && j != nil && isTerminalState(j.State) {
			if f != nil {
				if err := drainLogs(f, buf, stream); err != nil {
					return err
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func drainLogs(f *os.File, buf []byte, stream grpc.ServerStreamingServer[skipperv1.LogChunk]) error {
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if e := stream.Send(&skipperv1.LogChunk{Data: append([]byte(nil), buf[:n]...)}); e != nil {
				return e
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Error(codes.Internal, "read logs failed")
		}
	}
}

func ownerOrDefault(o string) string {
	if o == "" {
		return "anonymous"
	}
	return o
}

func isTerminalState(state string) bool {
	switch state {
	case store.JobCompleted, store.JobFailed, store.JobCancelled, store.JobTimeout:
		return true
	}
	return false
}
