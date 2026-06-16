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

	switch args[0] {
	case "nodes":
		if err := cmdNodes(*server); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("skctl", version.Version)
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `skctl - Skipper 命令行

用法:
  skctl [--server addr] <command>

命令:
  nodes      列出集群节点及资源
  version    打印版本

全局参数:
  --server   控制平面地址 (默认 127.0.0.1:7443, 可用 SKIPPER_SERVER_ADDR 覆盖)
`)
}

func cmdNodes(server string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := skipperv1.NewClusterServiceClient(conn).ListNodes(ctx, &skipperv1.ListNodesRequest{})
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tPARTITION\tCPUS\tMEM\tDEVICES\tHEARTBEAT\tVERSION")
	for _, n := range resp.GetNodes() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\n",
			n.GetName(), n.GetState(), n.GetPartition(),
			n.GetResources().GetCpus(), humanBytes(n.GetResources().GetMemTotalBytes()),
			len(n.GetResources().GetDevices()), heartbeatAge(n.GetLastHeartbeatUnix()), n.GetAgentVersion())
	}
	if len(resp.GetNodes()) == 0 {
		fmt.Fprintln(tw, "(no nodes registered)")
	}
	return tw.Flush()
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
