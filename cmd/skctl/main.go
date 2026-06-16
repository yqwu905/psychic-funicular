// Command skctl 是面向用户的命令行客户端。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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
  skctl [--server addr] <command>

命令:
  nodes      列出节点(库存/状态/心跳)
  top        节点实时负载(CPU%/内存/负载)
  gpu        列出各节点 GPU 设备实时指标
  npu        列出各节点 NPU 设备实时指标
  version    打印版本

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

// --- helpers ---

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
