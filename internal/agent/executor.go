package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

// executor 在本节点拉起并监管作业进程。
type executor struct {
	client  skipperv1.AgentServiceClient
	log     *slog.Logger
	mu      sync.Mutex
	running map[string]*runningJob
}

type runningJob struct {
	cancel context.CancelFunc
	pgid   int

	mu         sync.Mutex
	killReason string // "" | "cancel" | "timeout"
}

func (rj *runningJob) setReason(r string) {
	rj.mu.Lock()
	defer rj.mu.Unlock()
	if rj.killReason == "" {
		rj.killReason = r
	}
}

func (rj *runningJob) reason() string {
	rj.mu.Lock()
	defer rj.mu.Unlock()
	return rj.killReason
}

func newExecutor(client skipperv1.AgentServiceClient, log *slog.Logger) *executor {
	return &executor{client: client, log: log, running: make(map[string]*runningJob)}
}

// start 异步启动一个作业；重复的 jobID 会被忽略（去重）。
func (e *executor) start(a *skipperv1.Assignment) {
	e.mu.Lock()
	if _, ok := e.running[a.GetJobId()]; ok {
		e.mu.Unlock()
		return
	}
	rj := &runningJob{}
	e.running[a.GetJobId()] = rj
	e.mu.Unlock()
	go e.run(a, rj)
}

// cancel 终止一个正在运行的作业。
func (e *executor) cancel(jobID string) {
	e.mu.Lock()
	rj, ok := e.running[jobID]
	e.mu.Unlock()
	if !ok {
		return
	}
	rj.setReason("cancel")
	if rj.cancel != nil {
		rj.cancel()
	}
}

func (e *executor) run(a *skipperv1.Assignment, rj *runningJob) {
	jobID := a.GetJobId()
	defer func() {
		e.mu.Lock()
		delete(e.running, jobID)
		e.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	rj.cancel = cancel
	defer cancel()

	workdir := a.GetWorkdir()
	if workdir == "" {
		workdir = filepath.Join(os.TempDir(), "skipper-jobs", jobID)
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		e.report(jobID, store.JobFailed, -1, "workdir: "+err.Error())
		return
	}

	cmd := exec.Command("/bin/sh", "-c", a.GetCommand())
	cmd.Dir = workdir
	cmd.Env = buildEnv(a)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 独立进程组，便于整组终止

	pr, pw, err := os.Pipe()
	if err != nil {
		e.report(jobID, store.JobFailed, -1, "pipe: "+err.Error())
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		e.report(jobID, store.JobFailed, -1, "start: "+err.Error())
		return
	}
	_ = pw.Close() // 父进程关闭写端，子进程退出后读端得到 EOF
	rj.pgid = cmd.Process.Pid

	logsDone := make(chan struct{})
	go func() {
		e.pumpLogs(jobID, pr)
		close(logsDone)
	}()

	e.report(jobID, store.JobRunning, 0, "")

	var timedOut atomic.Bool
	if a.GetWalltimeSec() > 0 {
		timer := time.AfterFunc(time.Duration(a.GetWalltimeSec())*time.Second, func() {
			timedOut.Store(true)
			killGroup(rj.pgid)
		})
		defer timer.Stop()
	}
	go func() {
		<-ctx.Done()
		killGroup(rj.pgid)
	}()

	waitErr := cmd.Wait()
	_ = pr.Close()
	<-logsDone

	state, code, reason := classify(waitErr, rj.reason(), timedOut.Load())
	e.report(jobID, state, code, reason)
}

// buildEnv 组装作业环境：透传基础环境 + 自定义 env + 设备可见性隔离。
func buildEnv(a *skipperv1.Assignment) []string {
	env := os.Environ()
	for k, v := range a.GetEnv() {
		env = append(env, k+"="+v)
	}
	var gpus, npus []string
	for _, d := range a.GetDevices() {
		switch d.GetKind() {
		case "gpu":
			gpus = append(gpus, strconv.Itoa(int(d.GetIndex())))
		case "npu":
			npus = append(npus, strconv.Itoa(int(d.GetIndex())))
		}
	}
	// 即便为空也设置：未分配则作业看不到任何设备，实现隔离。
	env = append(env,
		"CUDA_VISIBLE_DEVICES="+strings.Join(gpus, ","),
		"ASCEND_RT_VISIBLE_DEVICES="+strings.Join(npus, ","),
		"SKIPPER_JOB_ID="+a.GetJobId(),
	)
	return env
}

// classify 根据退出错误与终止原因得出作业终态。
func classify(waitErr error, reason string, timedOut bool) (state string, code int32, why string) {
	var exitCode int32
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = int32(ee.ExitCode()) // 被信号杀死时为 -1
		} else {
			return store.JobFailed, -1, waitErr.Error()
		}
	}
	switch {
	case reason == "cancel":
		return store.JobCancelled, exitCode, "cancelled by user"
	case timedOut:
		return store.JobTimeout, exitCode, "walltime exceeded"
	case waitErr == nil:
		return store.JobCompleted, 0, ""
	default:
		return store.JobFailed, exitCode, ""
	}
}

// killGroup 先 SIGTERM 整组，5s 宽限后 SIGKILL。
func killGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.AfterFunc(5*time.Second, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
}

func (e *executor) pumpLogs(jobID string, r io.Reader) {
	buf := make([]byte, 16*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, aerr := e.client.AppendLogs(context.Background(), &skipperv1.AppendLogsRequest{
				JobId: jobID, Data: append([]byte(nil), buf[:n]...),
			}); aerr != nil {
				e.log.Warn("append logs failed", "job", jobID, "err", aerr)
			}
		}
		if err != nil {
			return
		}
	}
}

func (e *executor) report(jobID, state string, code int32, reason string) {
	if _, err := e.client.UpdateJobStatus(context.Background(), &skipperv1.UpdateJobStatusRequest{
		JobId: jobID, State: state, ExitCode: code, Reason: reason,
	}); err != nil {
		e.log.Warn("update job status failed", "job", jobID, "state", state, "err", err)
		return
	}
	e.log.Info("job state", "job", jobID, "state", state, "exit", code)
}
