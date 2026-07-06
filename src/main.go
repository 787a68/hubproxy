package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"hubproxy/config"
	"hubproxy/handlers"
	"hubproxy/utils"
)

//go:embed public/*
var staticFiles embed.FS

var (
	globalLimiter    *utils.IPRateLimiter
	serviceStartTime = time.Now()
)

// buildVersion 构建时通过 ldflags 注入；为空则回退到当天日期
var buildVersion = ""

// AppVersion 返回应用版本；buildVersion 由构建系统通过 ldflags 注入
func AppVersion() string {
	if v := buildVersion; v != "" {
		return v
	}
	return time.Now().Format("2006.01.02")
}

// serveEmbedFile 提供嵌入的静态文件
func serveEmbedFile(c *gin.Context, filename string) {
	data, err := staticFiles.ReadFile(filename)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	contentType := "text/html; charset=utf-8"
	switch {
	case strings.HasSuffix(filename, ".ico"):
		contentType = "image/x-icon"
	case strings.HasSuffix(filename, ".css"):
		contentType = "text/css; charset=utf-8"
	case strings.HasSuffix(filename, ".js"):
		contentType = "application/javascript; charset=utf-8"
	}
	c.Data(http.StatusOK, contentType, data)
}

func buildRouter(cfg *config.AppConfig) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	// 全局中间件：panic 恢复 → 请求 ID → 限流 → 结构化访问日志
	router.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		utils.Logger().Error("panic recovered", "err", recovered, "req_id", utils.GetRequestID(c))
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Internal server error",
			"code":  "INTERNAL_ERROR",
		})
	}))
	router.Use(utils.RequestIDMiddleware())
	router.Use(utils.RateLimitMiddleware(globalLimiter))
	router.Use(utils.RequestLogger(5 * time.Second))

	// 健康检查 / 指标
	router.GET("/healthz", healthzHandler)
	router.GET("/metrics", utils.MetricsHandler)

	// 镜像离线包
	handlers.InitImageTarRoutes(router)

	// 前端静态资源
	if cfg.Server.EnableFrontend {
		router.GET("/", func(c *gin.Context) { serveEmbedFile(c, "public/index.html") })
		router.GET("/public/*filepath", func(c *gin.Context) {
			serveEmbedFile(c, "public/"+strings.TrimPrefix(c.Param("filepath"), "/"))
		})
		router.GET("/images.html", func(c *gin.Context) { serveEmbedFile(c, "public/images.html") })
		router.GET("/search.html", func(c *gin.Context) { serveEmbedFile(c, "public/search.html") })
		router.GET("/favicon.ico", func(c *gin.Context) { serveEmbedFile(c, "public/favicon.ico") })
	} else {
		for _, p := range []string{"/", "/public/*filepath", "/images.html", "/search.html", "/favicon.ico"} {
			router.GET(p, func(c *gin.Context) { c.Status(http.StatusNotFound) })
		}
	}

	// Docker 镜像搜索
	handlers.RegisterSearchRoute(router)

	// Docker Registry v2 + 认证
	router.Any("/token", handlers.ProxyDockerAuthGin)
	router.Any("/token/*path", handlers.ProxyDockerAuthGin)
	router.Any("/v2/*path", handlers.ProxyDockerRegistryGin)

	// GitHub 文件代理（fallback）
	router.NoRoute(handlers.GitHubProxyHandler)

	return router
}

func healthzHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":          "ok",
		"service":         "hubproxy",
		"version":         AppVersion(),
		"start_time_unix": serviceStartTime.Unix(),
		"uptime_sec":      time.Since(serviceStartTime).Seconds(),
	})
}

func main() {
	// 初始化结构化日志
	slog.SetDefault(utils.Logger())

	if err := config.LoadConfig(); err != nil {
		log.Fatalf("配置加载失败: %v", err)
	}

	utils.InitHTTPClients()
	globalLimiter = utils.InitGlobalLimiter()
	handlers.InitDockerProxy()
	handlers.InitImageStreamer()
	handlers.InitDebouncer()

	cfg := config.GetConfig()
	router := buildRouter(cfg)

	utils.Logger().Info("starting hubproxy",
		"addr", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		"rate_limit", cfg.RateLimit.RequestLimit,
		"period_hours", cfg.RateLimit.PeriodHours,
		"h2c", cfg.Server.EnableH2C,
		"version", AppVersion(),
	)

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 30 * time.Minute, // 流式 tar 下载需要长时间写入
		IdleTimeout:  120 * time.Second,
	}

	if cfg.Server.EnableH2C {
		server.Handler = h2c.NewHandler(router, &http2.Server{
			MaxConcurrentStreams:         250,
			IdleTimeout:                  300 * time.Second,
			MaxReadFrameSize:             4 << 20,
			MaxUploadBufferPerConnection: 8 << 20,
			MaxUploadBufferPerStream:     2 << 20,
		})
	}

	// 优雅关闭
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		utils.Logger().Info("shutting down", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			utils.Logger().Error("graceful shutdown failed", "err", err)
		}
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("启动服务失败: %v", err)
	}
	utils.Logger().Info("server stopped")
}
