// Command skipper-agent 是节点代理入口。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/yqwu905/psychic-funicular/internal/agent"
	"github.com/yqwu905/psychic-funicular/internal/config"
	logpkg "github.com/yqwu905/psychic-funicular/internal/log"
	"github.com/yqwu905/psychic-funicular/internal/version"
)

func main() {
	cfgPath := flag.String("config", "", "path to agent.yaml")
	serverAddr := flag.String("server", "", "control-plane address (overrides config)")
	nodeName := flag.String("name", "", "node name (overrides config; default hostname)")
	partition := flag.String("partition", "", "partition (overrides config)")
	flag.Parse()

	cfg, err := config.LoadAgent(*cfgPath)
	if err != nil {
		panic(err)
	}
	if *serverAddr != "" {
		cfg.Server.Addr = *serverAddr
	}
	if *nodeName != "" {
		cfg.Node.Name = *nodeName
	}
	if *partition != "" {
		cfg.Node.Partition = *partition
	}

	logger := logpkg.New(cfg.Log.Level)
	logger.Info("skipper-agent starting", "version", version.Version, "server", cfg.Server.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.Run(ctx, cfg, logger); err != nil {
		logger.Error("agent exited", "err", err)
		os.Exit(1)
	}
	logger.Info("agent stopped")
}
