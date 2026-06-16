// Command skipper-server 是控制平面入口。
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/yqwu905/psychic-funicular/internal/config"
	logpkg "github.com/yqwu905/psychic-funicular/internal/log"
	"github.com/yqwu905/psychic-funicular/internal/server"
	"github.com/yqwu905/psychic-funicular/internal/store"
	"github.com/yqwu905/psychic-funicular/internal/version"
)

func main() {
	cfgPath := flag.String("config", "", "path to server.yaml")
	flag.Parse()

	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		// 日志器尚未建立，直接退出。
		panic(err)
	}
	logger := logpkg.New(cfg.Log.Level)
	logger.Info("skipper-server starting", "version", version.Version,
		"grpc", cfg.Listen.GRPC, "store", cfg.Store.DSN)

	st, err := store.OpenSQLite(cfg.Store.DSN)
	if err != nil {
		logger.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, logger, st)
	if err := srv.Run(ctx); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
