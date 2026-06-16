package server

import (
	"context"
	"fmt"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/event"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *agentService) PollAssignments(ctx context.Context, req *skipperv1.PollAssignmentsRequest) (*skipperv1.PollAssignmentsResponse, error) {
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	node, err := s.store.GetNode(ctx, req.GetNodeId())
	if err != nil {
		return nil, status.Error(codes.Internal, "lookup node failed")
	}
	if node == nil {
		return &skipperv1.PollAssignmentsResponse{ShouldReregister: true}, nil
	}

	run, err := s.store.ListJobsByNodeState(ctx, req.GetNodeId(), store.JobAssigned)
	if err != nil {
		return nil, status.Error(codes.Internal, "list assigned failed")
	}
	cancelling, err := s.store.ListJobsByNodeState(ctx, req.GetNodeId(), store.JobCancelling)
	if err != nil {
		return nil, status.Error(codes.Internal, "list cancelling failed")
	}

	resp := &skipperv1.PollAssignmentsResponse{}
	for _, j := range run {
		resp.Run = append(resp.Run, assignmentToProto(j))
	}
	for _, j := range cancelling {
		resp.CancelJobIds = append(resp.CancelJobIds, j.ID)
	}
	return resp, nil
}

func (s *agentService) UpdateJobStatus(ctx context.Context, req *skipperv1.UpdateJobStatusRequest) (*skipperv1.UpdateJobStatusResponse, error) {
	if req.GetJobId() == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id is required")
	}
	now := time.Now()
	switch req.GetState() {
	case store.JobRunning:
		if err := s.store.MarkJobRunning(ctx, req.GetJobId(), now); err != nil {
			return nil, status.Error(codes.Internal, "mark running failed")
		}
	case store.JobCompleted, store.JobFailed, store.JobCancelled, store.JobTimeout:
		if err := s.store.FinishJob(ctx, req.GetJobId(), req.GetState(), req.GetExitCode(), req.GetReason(), now); err != nil {
			return nil, status.Error(codes.Internal, "finish job failed")
		}
		s.emitJobEvent(ctx, req.GetJobId(), req.GetState(), req.GetExitCode())
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown state %q", req.GetState())
	}
	s.log.Info("job status", "job", req.GetJobId(), "state", req.GetState(),
		"exit_code", req.GetExitCode(), "reason", req.GetReason())
	return &skipperv1.UpdateJobStatusResponse{Ok: true}, nil
}

// emitJobEvent 在作业进入终态时发「任务结束」事件，通知提交者（CANCELLED 由用户主动，不通知）。
func (s *agentService) emitJobEvent(ctx context.Context, jobID, state string, code int32) {
	if state == store.JobCancelled {
		return
	}
	j, err := s.store.GetJob(ctx, jobID)
	if err != nil || j == nil {
		return
	}
	typ, sev := event.TypeJobCompleted, event.SevInfo
	if state != store.JobCompleted {
		typ, sev = event.TypeJobFailed, event.SevWarning
	}
	name := j.Name
	if name == "" {
		name = jobID
	}
	s.events.Emit(ctx, event.Event{
		Type: typ, Severity: sev, Source: jobID,
		Summary:  fmt.Sprintf("作业 %s %s，退出码 %d", name, state, code),
		DedupKey: "job|" + jobID,
		Labels: map[string]string{
			"job": jobID, "name": j.Name, "owner": j.Owner,
			"node": j.NodeName, "state": state, "exit_code": fmt.Sprintf("%d", code),
		},
	})
}

func (s *agentService) AppendLogs(_ context.Context, req *skipperv1.AppendLogsRequest) (*skipperv1.AppendLogsResponse, error) {
	if req.GetJobId() == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id is required")
	}
	if err := s.jobLogs.append(req.GetJobId(), req.GetData()); err != nil {
		s.log.Error("append logs failed", "job", req.GetJobId(), "err", err)
		return nil, status.Error(codes.Internal, "append logs failed")
	}
	return &skipperv1.AppendLogsResponse{Ok: true}, nil
}
