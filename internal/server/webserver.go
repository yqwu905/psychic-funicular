package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/event"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"github.com/yqwu905/psychic-funicular/internal/webui"
)

// webServer 提供 Web 控制台所需的 JSON API，并托管内嵌的 SPA 静态资源。
// 它直接复用控制平面的 store / metrics / 作业日志，与 gRPC 服务共享同一份数据。
type webServer struct {
	store   store.Store
	metrics *metrics.Store
	jobLogs *jobLogStore
	log     *slog.Logger
}

// handler 装配 /api/v1 路由与静态资源服务。
func (w *webServer) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/nodes", w.listNodes)
	mux.HandleFunc("GET /api/v1/jobs", w.listJobs)
	mux.HandleFunc("POST /api/v1/jobs", w.submitJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}", w.getJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", w.cancelJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", w.jobLogsStream)
	mux.HandleFunc("GET /api/v1/events", w.listEvents)
	mux.HandleFunc("GET /api/v1/notifications", w.listNotifications)
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})

	mux.Handle("/", spaHandler(webui.Assets()))
	return mux
}

// ---- JSON DTO ----

type cpuJSON struct {
	Utilization float64 `json:"utilization"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	Cores       uint32  `json:"cores"`
}

type memJSON struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

type diskJSON struct {
	Mount             string  `json:"mount"`
	Fstype            string  `json:"fstype"`
	TotalBytes        uint64  `json:"total_bytes"`
	UsedBytes         uint64  `json:"used_bytes"`
	UsedPercent       float64 `json:"used_percent"`
	InodesUsedPercent float64 `json:"inodes_used_percent"`
}

type deviceJSON struct {
	Kind          string  `json:"kind"`
	Vendor        string  `json:"vendor"`
	Index         uint32  `json:"index"`
	UUID          string  `json:"uuid"`
	Name          string  `json:"name"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	Utilization   float64 `json:"utilization"`
	TemperatureC  float64 `json:"temperature_c"`
	PowerWatts    float64 `json:"power_watts"`
}

type nodeJSON struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Partition         string            `json:"partition"`
	State             string            `json:"state"`
	Addr              string            `json:"addr"`
	AgentVersion      string            `json:"agent_version"`
	LastHeartbeatUnix int64             `json:"last_heartbeat_unix"`
	CPUs              uint32            `json:"cpus"`
	MemTotalBytes     uint64            `json:"mem_total_bytes"`
	Labels            map[string]string `json:"labels"`
	HasMetrics        bool              `json:"has_metrics"`
	MetricsAtUnix     int64             `json:"metrics_at_unix"`
	CPU               *cpuJSON          `json:"cpu"`
	Mem               *memJSON          `json:"mem"`
	Disks             []diskJSON        `json:"disks"`
	Devices           []deviceJSON      `json:"devices"`
}

type reqJSON struct {
	CPUs        uint32 `json:"cpus"`
	MemBytes    uint64 `json:"mem_bytes"`
	GPUs        uint32 `json:"gpus"`
	GPUType     string `json:"gpu_type"`
	WalltimeSec int64  `json:"walltime_sec"`
}

type allocJSON struct {
	Kind  string `json:"kind"`
	Index uint32 `json:"index"`
}

type jobJSON struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Owner        string            `json:"owner"`
	Partition    string            `json:"partition"`
	State        string            `json:"state"`
	Priority     int32             `json:"priority"`
	Command      string            `json:"command"`
	Workdir      string            `json:"workdir"`
	Env          map[string]string `json:"env"`
	Request      reqJSON           `json:"request"`
	NodeID       string            `json:"node_id"`
	NodeName     string            `json:"node_name"`
	Devices      []allocJSON       `json:"devices"`
	ExitCode     int32             `json:"exit_code"`
	Reason       string            `json:"reason"`
	SubmitAtUnix int64             `json:"submit_at_unix"`
	StartAtUnix  int64             `json:"start_at_unix"`
	EndAtUnix    int64             `json:"end_at_unix"`
}

type eventJSON struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Severity string            `json:"severity"`
	Source   string            `json:"source"`
	Summary  string            `json:"summary"`
	Labels   map[string]string `json:"labels"`
	TimeUnix int64             `json:"time_unix"`
}

type notifJSON struct {
	ID         string `json:"id"`
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	Rule       string `json:"rule"`
	Channel    string `json:"channel"`
	Recipients string `json:"recipients"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	Summary    string `json:"summary"`
	TimeUnix   int64  `json:"time_unix"`
}

// ---- handlers ----

func (w *webServer) listNodes(rw http.ResponseWriter, r *http.Request) {
	nodes, err := w.store.ListNodes(r.Context())
	if err != nil {
		w.fail(rw, http.StatusInternalServerError, "list nodes failed")
		return
	}
	out := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, w.nodeToJSON(n))
	}
	writeJSON(rw, map[string]any{"nodes": out})
}

func (w *webServer) nodeToJSON(n *store.Node) nodeJSON {
	nj := nodeJSON{
		ID: n.ID, Name: n.Name, Partition: n.Partition, State: n.State, Addr: n.Addr,
		AgentVersion: n.AgentVersion, CPUs: n.CPUs, MemTotalBytes: n.MemTotalBytes, Labels: n.Labels,
	}
	if nj.Labels == nil {
		nj.Labels = map[string]string{}
	}
	if !n.LastHeartbeat.IsZero() {
		nj.LastHeartbeatUnix = n.LastHeartbeat.Unix()
	}

	snap, ok := w.metrics.Get(n.ID)
	if ok && snap != nil {
		nj.HasMetrics = true
		nj.MetricsAtUnix = snap.GetTimestampUnix()
		if c := snap.GetCpu(); c != nil {
			nj.CPU = &cpuJSON{Utilization: c.GetUtilization(), Load1: c.GetLoad1(), Load5: c.GetLoad5(), Load15: c.GetLoad15(), Cores: c.GetCores()}
		}
		if m := snap.GetMem(); m != nil {
			nj.Mem = &memJSON{TotalBytes: m.GetTotalBytes(), UsedBytes: m.GetUsedBytes(), AvailableBytes: m.GetAvailableBytes(), UsedPercent: m.GetUsedPercent()}
		}
		for _, d := range snap.GetDisks() {
			nj.Disks = append(nj.Disks, diskJSON{
				Mount: d.GetMount(), Fstype: d.GetFstype(), TotalBytes: d.GetTotalBytes(),
				UsedBytes: d.GetUsedBytes(), UsedPercent: d.GetUsedPercent(), InodesUsedPercent: d.GetInodesUsedPercent(),
			})
		}
	}
	nj.Devices = w.nodeDevices(n, snap)
	return nj
}

// nodeDevices 合并设备：优先用实时快照(含利用率/温度/功耗)，否则回退到静态库存。
func (w *webServer) nodeDevices(n *store.Node, snap *skipperv1.MetricsSnapshot) []deviceJSON {
	if snap != nil && len(snap.GetDevices()) > 0 {
		out := make([]deviceJSON, 0, len(snap.GetDevices()))
		for _, d := range snap.GetDevices() {
			out = append(out, deviceJSON{
				Kind: d.GetKind(), Vendor: d.GetVendor(), Index: d.GetIndex(), UUID: d.GetUuid(), Name: d.GetName(),
				MemTotalBytes: d.GetMemTotalBytes(), MemUsedBytes: d.GetMemUsedBytes(),
				Utilization: d.GetUtilization(), TemperatureC: d.GetTemperatureC(), PowerWatts: d.GetPowerWatts(),
			})
		}
		return out
	}
	out := make([]deviceJSON, 0, len(n.Devices))
	for _, d := range n.Devices {
		out = append(out, deviceJSON{
			Kind: d.Kind, Vendor: d.Vendor, Index: d.Index, UUID: d.UUID, Name: d.Name, MemTotalBytes: d.MemTotalBytes,
		})
	}
	return out
}

func (w *webServer) listJobs(rw http.ResponseWriter, r *http.Request) {
	state := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("state")))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	jobs, err := w.store.ListJobs(r.Context(), state, owner)
	if err != nil {
		w.fail(rw, http.StatusInternalServerError, "list jobs failed")
		return
	}
	out := make([]jobJSON, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobToJSON(j))
	}
	writeJSON(rw, map[string]any{"jobs": out})
}

func (w *webServer) getJob(rw http.ResponseWriter, r *http.Request) {
	j, err := w.store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		w.fail(rw, http.StatusInternalServerError, "get job failed")
		return
	}
	if j == nil {
		w.fail(rw, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(rw, jobToJSON(j))
}

// submitReq 是 Web 提交作业的请求体（内存/时长支持人类可读写法，如 "64G" / "24h"）。
type submitReq struct {
	Name      string            `json:"name"`
	Owner     string            `json:"owner"`
	Partition string            `json:"partition"`
	Priority  int32             `json:"priority"`
	Command   string            `json:"command"`
	Workdir   string            `json:"workdir"`
	CPUs      uint32            `json:"cpus"`
	Mem       string            `json:"mem"`
	GPUs      uint32            `json:"gpus"`
	GPUType   string            `json:"gpu_type"`
	Walltime  string            `json:"walltime"`
	Env       map[string]string `json:"env"`
}

func (w *webServer) submitJob(rw http.ResponseWriter, r *http.Request) {
	var req submitReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		w.fail(rw, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		w.fail(rw, http.StatusBadRequest, "command is required")
		return
	}
	memBytes, err := parseSize(req.Mem)
	if err != nil {
		w.fail(rw, http.StatusBadRequest, "invalid mem: "+err.Error())
		return
	}
	walltime, err := parseWalltime(req.Walltime)
	if err != nil {
		w.fail(rw, http.StatusBadRequest, "invalid walltime: "+err.Error())
		return
	}
	j := &store.Job{
		ID: store.NewID(), Name: req.Name, Owner: ownerOrDefault(req.Owner), Partition: req.Partition,
		State: store.JobPending, Priority: req.Priority, Command: req.Command, Env: req.Env, Workdir: req.Workdir,
		ReqCPUs: req.CPUs, ReqMemBytes: memBytes, ReqGPUs: req.GPUs, GPUType: req.GPUType,
		WalltimeSec: walltime, SubmitAt: time.Now(),
	}
	if err := w.store.CreateJob(r.Context(), j); err != nil {
		w.fail(rw, http.StatusInternalServerError, "submit job failed")
		return
	}
	w.log.Info("web job submitted", "job", j.ID, "name", j.Name, "owner", j.Owner, "partition", j.Partition, "gpus", j.ReqGPUs)
	rw.WriteHeader(http.StatusCreated)
	writeJSON(rw, map[string]any{"job_id": j.ID})
}

func (w *webServer) cancelJob(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state, err := w.store.CancelJob(r.Context(), id)
	if err != nil {
		w.fail(rw, http.StatusNotFound, err.Error())
		return
	}
	w.log.Info("web job cancel requested", "job", id, "new_state", state)
	writeJSON(rw, map[string]any{"ok": true, "state": state})
}

// jobLogsStream 以纯文本流式返回作业日志；follow=true 时持续推送直到作业终态或客户端断开。
func (w *webServer) jobLogsStream(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := w.store.GetJob(r.Context(), id)
	if err != nil {
		w.fail(rw, http.StatusInternalServerError, "get job failed")
		return
	}
	if job == nil {
		w.fail(rw, http.StatusNotFound, "job not found")
		return
	}
	follow := r.URL.Query().Get("follow") == "1" || r.URL.Query().Get("follow") == "true"

	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := rw.(http.Flusher)

	var f *os.File
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	buf := make([]byte, 32*1024)
	ctx := r.Context()
	for {
		if f == nil {
			file, oerr := w.jobLogs.open(id)
			switch {
			case oerr == nil:
				f = file
			case !os.IsNotExist(oerr):
				return
			}
		}
		if f != nil {
			if drainErr := drainLogsHTTP(f, buf, rw, flusher); drainErr != nil {
				return
			}
		}
		if !follow {
			return
		}
		if j, gerr := w.store.GetJob(ctx, id); gerr == nil && j != nil && isTerminalState(j.State) {
			if f != nil {
				_ = drainLogsHTTP(f, buf, rw, flusher)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func drainLogsHTTP(f *os.File, buf []byte, rw http.ResponseWriter, flusher http.Flusher) error {
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := rw.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (w *webServer) listEvents(rw http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	events, err := w.store.ListEvents(r.Context(), limit)
	if err != nil {
		w.fail(rw, http.StatusInternalServerError, "list events failed")
		return
	}
	out := make([]eventJSON, 0, len(events))
	for _, e := range events {
		out = append(out, eventToJSON(e))
	}
	writeJSON(rw, map[string]any{"events": out})
}

func (w *webServer) listNotifications(rw http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	ns, err := w.store.ListNotifications(r.Context(), limit)
	if err != nil {
		w.fail(rw, http.StatusInternalServerError, "list notifications failed")
		return
	}
	out := make([]notifJSON, 0, len(ns))
	for _, n := range ns {
		out = append(out, notifToJSON(n))
	}
	writeJSON(rw, map[string]any{"notifications": out})
}

// ---- conversions ----

func jobToJSON(j *store.Job) jobJSON {
	devs := make([]allocJSON, 0, len(j.Devices))
	for _, d := range j.Devices {
		devs = append(devs, allocJSON{Kind: d.Kind, Index: d.Index})
	}
	env := j.Env
	if env == nil {
		env = map[string]string{}
	}
	return jobJSON{
		ID: j.ID, Name: j.Name, Owner: j.Owner, Partition: j.Partition, State: j.State, Priority: j.Priority,
		Command: j.Command, Workdir: j.Workdir, Env: env,
		Request: reqJSON{CPUs: j.ReqCPUs, MemBytes: j.ReqMemBytes, GPUs: j.ReqGPUs, GPUType: j.GPUType, WalltimeSec: j.WalltimeSec},
		NodeID:  j.NodeID, NodeName: j.NodeName, Devices: devs, ExitCode: j.ExitCode, Reason: j.Reason,
		SubmitAtUnix: unixOrZero(j.SubmitAt), StartAtUnix: unixOrZero(j.StartAt), EndAtUnix: unixOrZero(j.EndAt),
	}
}

func eventToJSON(e *event.Event) eventJSON {
	labels := e.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return eventJSON{
		ID: e.ID, Type: e.Type, Severity: string(e.Severity), Source: e.Source, Summary: e.Summary,
		Labels: labels, TimeUnix: unixOrZero(e.Time),
	}
}

func notifToJSON(n *event.Notification) notifJSON {
	return notifJSON{
		ID: n.ID, EventID: n.EventID, EventType: n.EventType, Rule: n.Rule, Channel: n.Channel,
		Recipients: n.Recipients, Status: n.Status, Error: n.Error, Summary: n.Summary, TimeUnix: unixOrZero(n.Time),
	}
}

// ---- helpers ----

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(v)
}

func (w *webServer) fail(rw http.ResponseWriter, code int, msg string) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(map[string]any{"error": msg})
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// parseWalltime 解析 "12h"/"30m"/"90s"；空或 "0" 表示不限(0)。
func parseWalltime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, errors.New("negative duration")
	}
	return int64(d.Seconds()), nil
}

// parseSize 解析 "32G"/"512M"/"2Gi"/"1024"(字节)，K/M/G/T 按 1024 计；空或 "0" 返回 0。
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	s = strings.TrimSuffix(strings.ToUpper(s), "B")
	s = strings.TrimSuffix(s, "I")
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "T"):
		mult, s = 1<<40, strings.TrimSuffix(s, "T")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("negative size")
	}
	return uint64(n * float64(mult)), nil
}

// spaHandler 托管内嵌 SPA：命中文件则返回该文件，否则回退到 index.html(单页应用)。
func spaHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := assets.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(rw, r)
			return
		}
		// 未知路径回退到 index.html，交给前端路由。
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(rw, r2)
	})
}
