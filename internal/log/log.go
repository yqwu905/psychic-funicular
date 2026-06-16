// Package log 提供统一的结构化日志构造。
package log

import (
	"log/slog"
	"os"
	"strings"
)

// New 按级别字符串(debug/info/warn/error)构造一个写到 stderr 的 slog.Logger。
func New(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	return slog.New(h)
}
