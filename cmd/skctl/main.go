// Command skctl 是面向用户的命令行客户端。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"github.com/yqwu905/psychic-funicular/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	server := flag.String("server", envOr("SKIPPER_SERVER_ADDR", "127.0.0.1:7443"), "control-plane address")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "nodes":
		err = cmdNodes(*server)
	case "top":
		err = cmdTop(*server)
	case "gpu":
		err = cmdDevices(*server, "gpu")
	case "npu":
		err = cmdDevices(*server, "npu")
	case "submit":
		err = cmdSubmit(*server, args[1:])
	case "queue":
		err = cmdQueue(*server, args[1:])
	case "cancel":
		err = cmdCancel(*server, args[1:])
	case "logs":
		err = cmdLogs(*server, args[1:])
	case "events":
		err = cmdEvents(*server)
	case "notifications", "notify":
		err = cmdNotifications(*server)
	case "version":
		fmt.Println("skctl", version.Version)
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `skctl - Skipper 命令行

用法:
  skctl [--server addr] <command> [args]
  (注意: --server 需放在子命令之前)

命令:
  nodes                       列出节点(库存/状态/心跳)
  top                         节点实时负载(CPU%/内存/负载)
  gpu | npu                   列出各节点 GPU/NPU 设备实时指标
  submit [flags] -- <cmd...>  提交作业
  queue [--state S] [--me]    查看作业队列
  cancel <jobid>              取消作业
  logs [-f] <jobid>           查看/跟踪作业日志
  events                      查看最近事件(硬盘满/设备空置/任务结束/节点失联)
  notifications               查看最近通知投递记录
  version                     打印版本

submit 参数:
  --name --partition --cpus --mem(如 32G) --gpus --gpu-type
  --time(如 12h) --priority --workdir --owner --env K=V(可重复)

全局参数:
  --server   控制平面地址 (默认 127.0.0.1:7443, 可用 SKIPPER_SERVER_ADDR 覆盖)
`)
}

func cmdNodes(server string) error {
	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()

	resp, err := skipperv1.NewClusterServiceClient(conn).ListNodes(ctx, &skipperv1.ListNodesRequest{})
	if err != nil {
		return err
	}
	tw := newTable()
	fmt.Fprintln(tw, "NAME\tSTATE\tPARTITION\tCPUS\tMEM\tGPU\tNPU\tHEARTBEAT\tVERSION")
	for _, n := range resp.GetNodes() {
		gpu, npu := countDevices(n.GetResources().GetDevices())
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%d\t%d\t%s\t%s\n",
			n.GetName(), n.GetState(), n.GetPartition(),
			n.GetResources().GetCpus(), humanBytes(n.GetResources().GetMemTotalBytes()),
			gpu, npu, heartbeatAge(n.GetLastHeartbeatUnix()), n.GetAgentVersion())
	}
	if len(resp.GetNodes()) == 0 {
		fmt.Fprintln(tw, "(no nodes registered)")
	}
	return tw.Flush()
}

func cmdTop(server string) error {
	nodes, err := listMetrics(server)
	if err != nil {
		return err
	}
	tw := newTable()
	fmt.Fprintln(tw, "NODE\tSTATE\tCPU%\tMEM\tMEM%\tLOAD1\tGPU\tNPU")
	for _, nm := range nodes {
		s := nm.GetSnapshot()
		cpu, mem := s.GetCpu(), s.GetMem()
		gpu, npu := countDeviceStats(s.GetDevices())
		fmt.Fprintf(tw, "%s\t%s\t%.0f\t%s/%s\t%.0f\t%.2f\t%d\t%d\n",
			nm.GetNodeName(), nm.GetState(), cpu.GetUtilization(),
			humanBytes(mem.GetUsedBytes()), humanBytes(mem.GetTotalBytes()),
			mem.GetUsedPercent(), cpu.GetLoad1(), gpu, npu)
	}
	if len(nodes) == 0 {
		fmt.Fprintln(tw, "(no nodes registered)")
	}
	return tw.Flush()
}

func cmdDevices(server, kind string) error {
	nodes, err := listMetrics(server)
	if err != nil {
		return err
	}
	tw := newTable()
	fmt.Fprintln(tw, "NODE\tIDX\tNAME\tUTIL%\tMEM\tTEMP\tPOWER")
	rows := 0
	for _, nm := range nodes {
		for _, d := range nm.GetSnapshot().GetDevices() {
			if d.GetKind() != kind {
				continue
			}
			rows++
			fmt.Fprintf(tw, "%s\t%d\t%s\t%.0f\t%s/%s\t%.0f°C\t%.0fW\n",
				nm.GetNodeName(), d.GetIndex(), d.GetName(), d.GetUtilization(),
				humanBytes(d.GetMemUsedBytes()), humanBytes(d.GetMemTotalBytes()),
				d.GetTemperatureC(), d.GetPowerWatts())
		}
	}
	if rows == 0 {
		fmt.Fprintf(tw, "(no %s devices reported)\n", kind)
	}
	return tw.Flush()
}

func cmdSubmit(server string, args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	name := fs.String("name", "", "job name")
	partition := fs.String("partition", "", "partition (empty matches any)")
	cpus := fs.Uint("cpus", 1, "CPU cores")
	memStr := fs.String("mem", "0", "memory, e.g. 32G")
	gpus := fs.Uint("gpus", 0, "accelerator count (GPU/NPU)")
	gpuType := fs.String("gpu-type", "", "device model constraint (advisory)")
	timeStr := fs.String("time", "0", "walltime, e.g. 12h (0=unlimited)")
	priority := fs.Int("priority", 0, "priority (higher runs first)")
	workdir := fs.String("workdir", "", "working directory")
	owner := fs.String("owner", "", "owner (default current user)")
	var envs stringSlice
	fs.Var(&envs, "env", "environment K=V (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("missing command (use: submit [flags] -- <command>)")
	}
	memBytes, err := parseSize(*memStr)
	if err != nil {
		return fmt.Errorf("invalid --mem: %w", err)
	}
	var walltime int64
	if *timeStr != "0" && *timeStr != "" {
		d, err := time.ParseDuration(*timeStr)
		if err != nil {
			return fmt.Errorf("invalid --time: %w", err)
		}
		walltime = int64(d.Seconds())
	}
	envMap, err := parseEnv(envs)
	if err != nil {
		return err
	}

	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()

	resp, err := skipperv1.NewClusterServiceClient(conn).SubmitJob(ctx, &skipperv1.SubmitJobRequest{
		Name:      *name,
		Owner:     ownerOrCurrent(*owner),
		Partition: *partition,
		Priority:  int32(*priority),
		Command:   command,
		Env:       envMap,
		Workdir:   *workdir,
		Request: &skipperv1.ResourceRequest{
			Cpus:        uint32(*cpus),
			MemBytes:    memBytes,
			Gpus:        uint32(*gpus),
			GpuType:     *gpuType,
			WalltimeSec: walltime,
		},
	})
	if err != nil {
		return err
	}
	fmt.Println("submitted job", resp.GetJobId())
	return nil
}

func cmdQueue(server string, args []string) error {
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	state := fs.String("state", "", "filter by state")
	owner := fs.String("owner", "", "filter by owner")
	me := fs.Bool("me", false, "only my jobs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	o := *owner
	if *me {
		o = ownerOrCurrent("")
	}

	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()

	resp, err := skipperv1.NewClusterServiceClient(conn).ListJobs(ctx, &skipperv1.ListJobsRequest{
		State: strings.ToUpper(*state), Owner: o,
	})
	if err != nil {
		return err
	}
	tw := newTable()
	fmt.Fprintln(tw, "JOBID\tNAME\tOWNER\tSTATE\tPART\tNODE\tPRIO\tCPUS\tGPUS\tRUNTIME")
	for _, j := range resp.GetJobs() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			shortID(j.GetId()), dash(j.GetName()), j.GetOwner(), j.GetState(),
			dash(j.GetPartition()), dash(j.GetNodeName()), j.GetPriority(),
			j.GetRequest().GetCpus(), j.GetRequest().GetGpus(), runtimeOf(j))
	}
	if len(resp.GetJobs()) == 0 {
		fmt.Fprintln(tw, "(no jobs)")
	}
	return tw.Flush()
}

func cmdCancel(server string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: cancel <jobid>")
	}
	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()

	resp, err := skipperv1.NewClusterServiceClient(conn).CancelJob(ctx, &skipperv1.CancelJobRequest{JobId: args[0]})
	if err != nil {
		return err
	}
	fmt.Printf("job %s -> %s\n", args[0], resp.GetState())
	return nil
}

func cmdLogs(server string, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow log output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: logs [-f] <jobid>")
	}
	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()

	stream, err := skipperv1.NewClusterServiceClient(conn).GetJobLogs(context.Background(),
		&skipperv1.GetJobLogsRequest{JobId: fs.Arg(0), Follow: *follow})
	if err != nil {
		return err
	}
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		_, _ = os.Stdout.Write(chunk.GetData())
	}
}

func cmdEvents(server string) error {
	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()
	resp, err := skipperv1.NewClusterServiceClient(conn).ListEvents(ctx, &skipperv1.ListEventsRequest{Limit: 50})
	if err != nil {
		return err
	}
	tw := newTable()
	fmt.Fprintln(tw, "TIME\tSEVERITY\tTYPE\tSOURCE\tSUMMARY")
	for _, e := range resp.GetEvents() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			tsShort(e.GetTimeUnix()), e.GetSeverity(), e.GetType(), e.GetSource(), e.GetSummary())
	}
	if len(resp.GetEvents()) == 0 {
		fmt.Fprintln(tw, "(no events)")
	}
	return tw.Flush()
}

func cmdNotifications(server string) error {
	conn, err := dial(server)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()
	resp, err := skipperv1.NewClusterServiceClient(conn).ListNotifications(ctx, &skipperv1.ListNotificationsRequest{Limit: 50})
	if err != nil {
		return err
	}
	tw := newTable()
	fmt.Fprintln(tw, "TIME\tTYPE\tCHANNEL\tTO\tSTATUS\tSUMMARY")
	for _, n := range resp.GetNotifications() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			tsShort(n.GetTimeUnix()), n.GetEventType(), n.GetChannel(),
			dash(n.GetRecipients()), n.GetStatus(), n.GetSummary())
	}
	if len(resp.GetNotifications()) == 0 {
		fmt.Fprintln(tw, "(no notifications)")
	}
	return tw.Flush()
}

// --- helpers ---

func tsShort(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	return time.Unix(unix, 0).Format("15:04:05")
}

func listMetrics(server string) ([]*skipperv1.NodeMetrics, error) {
	conn, err := dial(server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := timeoutCtx()
	defer cancel()
	resp, err := skipperv1.NewClusterServiceClient(conn).ListMetrics(ctx, &skipperv1.ListMetricsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetNodes(), nil
}

func dial(server string) (*grpc.ClientConn, error) {
	return grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func timeoutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
}

func countDevices(devs []*skipperv1.Device) (gpu, npu int) {
	for _, d := range devs {
		switch d.GetKind() {
		case "gpu":
			gpu++
		case "npu":
			npu++
		}
	}
	return gpu, npu
}

func countDeviceStats(devs []*skipperv1.DeviceStats) (gpu, npu int) {
	for _, d := range devs {
		switch d.GetKind() {
		case "gpu":
			gpu++
		case "npu":
			npu++
		}
	}
	return gpu, npu
}

// stringSlice 是可重复的字符串 flag。
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func parseEnv(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(items))
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --env %q (want K=V)", it)
		}
		m[k] = v
	}
	return m, nil
}

// parseSize 解析 "32G"/"512M"/"2Gi"/"1024"(字节)，K/M/G/T 按 1024 计。
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	s = strings.TrimSuffix(strings.ToUpper(s), "B")
	s = strings.TrimSuffix(s, "I") // 兼容 Gi/Mi
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
	return uint64(n * float64(mult)), nil
}

func ownerOrCurrent(o string) string {
	if o != "" {
		return o
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "anonymous"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runtimeOf(j *skipperv1.Job) string {
	start, end := j.GetStartAtUnix(), j.GetEndAtUnix()
	if start <= 0 {
		return "-"
	}
	var d time.Duration
	if end > 0 {
		d = time.Duration(end-start) * time.Second
	} else {
		d = time.Since(time.Unix(start, 0))
	}
	return d.Round(time.Second).String()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func heartbeatAge(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	d := time.Since(time.Unix(unix, 0)).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String() + " ago"
}
