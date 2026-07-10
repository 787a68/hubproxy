package utils

import (
	"io"
	"log/slog"
	"os"
	"sync"

	"hubproxy/config"
)

var (
	loggerOnce sync.Once
	logger     *slog.Logger
)

// getLogger 返回全局 slog 日志器（JSON 格式，同时输出到 stdout 和文件）
func getLogger() *slog.Logger {
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

		var w io.Writer = os.Stdout

		logFile := config.GetConfig().LogFile
		if logFile != "" {
			if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
				w = io.MultiWriter(os.Stdout, f)
			}
		}

		logger = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level: level,
		}))
	})
	return logger
}

// Logger 暴露全局 logger 供其他包使用
func Logger() *slog.Logger { return getLogger() }
