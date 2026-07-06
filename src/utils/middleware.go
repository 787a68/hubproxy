package utils

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gin-gonic/gin"
)

const requestIDHeader = "X-Request-ID"

// RequestIDMiddleware 注入请求 ID（用于日志关联）
// 复用客户端传入的 ID；缺失时生成 16 字节随机 hex
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

// RequestLogger 替代 gin.Logger，按结构化格式记录请求
// 仅记录慢请求（>= slowThreshold）和错误状态码，避免每请求日志开销
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

		// 仅记录异常情况（错误或慢）
		if status >= 400 || latency >= slowThreshold {
			ip := extractIP(c.ClientIP())
			logf().Info("request",
				"req_id", GetRequestID(c),
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"status", status,
				"latency", latency.String(),
				"ip", ip,
				"size", c.Writer.Size(),
			)
		}
	}
}
