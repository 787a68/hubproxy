package utils

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gin-gonic/gin"
)

const requestIDHeader = "X-Request-ID"

// RequestIDMiddleware 注入请求 ID（用于日志关联）
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if id == "" {
			buf := make([]byte, 16)
			_, _ = rand.Read(buf)
			id = hex.EncodeToString(buf)
		}
		c.Set("request_id", id)
		c.Writer.Header().Set(requestIDHeader, id)
		c.Next()
	}
}

// GetRequestID 从上下文取出 request_id
func GetRequestID(c *gin.Context) string {
	if v, ok := c.Get("request_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return "-"
}

// RequestLogger 请求日志中间件
// Debug: 记录所有请求
// Info: 仅记录慢请求
// Warn: 记录 4xx 客户端错误
// Error: 记录 5xx 服务端错误
func RequestLogger(slowThreshold time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		GlobalMetrics.RequestsTotal.Add(1)
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		status := c.Writer.Status()
		if status >= 400 {
			GlobalMetrics.RequestsErrors.Add(1)
		}

		reqFields := []any{
			"req_id", GetRequestID(c),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"latency", latency.String(),
			"ip", extractIP(c.ClientIP()),
			"size", c.Writer.Size(),
		}

		switch {
		case status >= 500:
			logf().Error("request", reqFields...)
		case status >= 400:
			logf().Warn("request", reqFields...)
		case latency >= slowThreshold:
			logf().Info("request", reqFields...)
		default:
			logf().Debug("request", reqFields...)
		}
	}
}
