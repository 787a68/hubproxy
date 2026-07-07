package utils

import (
	"log/slog"
	"os"
	"sync"

	"hubproxy/config"
)

var (
	loggerOnce sync.Once
	logger     *slog.Logger
)

// logf 返回全局 slog 日志器（JSON 格式）
func logf() *slog.Logger {
	loggerOnce.Do(func() {
		level := slog.LevelInfo
		switch config.GetConfig().LogLevel {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		}))
	})
	return logger
}

// Logger 暴露全局 logger 供其他包使用
func Logger() *slog.Logger { return logf() }
