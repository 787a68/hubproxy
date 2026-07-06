package utils

import (
	"log/slog"
	"os"
	"sync"
)

var (
	loggerOnce sync.Once
	logger     *slog.Logger
)

// logf 返回全局 slog 日志器（JSON 格式，可延展为按配置切换）
func logf() *slog.Logger {
	loggerOnce.Do(func() {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	})
	return logger
}

// Logger 暴露全局 logger 供其他包使用
func Logger() *slog.Logger { return logf() }
