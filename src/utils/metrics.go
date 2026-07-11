package utils

import (
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Metrics 全局指标计数器（atomic，无锁）
type Metrics struct {
	RequestsTotal      atomic.Int64
	RequestsErrors     atomic.Int64
	CacheHits          atomic.Int64
	CacheMisses        atomic.Int64
	BytesProxied       atomic.Int64
	DockerManifestReqs atomic.Int64
	DockerBlobReqs     atomic.Int64
	GitHubReqs         atomic.Int64
	SearchReqs         atomic.Int64
}

var GlobalMetrics = &Metrics{}

// processStartTime 进程启动时间，用于计算 uptime
var processStartTime = time.Now()

// runtimeStats 采集 Go 运行时指标（按需调用，非 hot path）
func runtimeStats() (goroutines, memAllocMB, memSysMB, heapObjects, uptimeSec int64) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(runtime.NumGoroutine()),
		int64(m.Alloc / 1024 / 1024),
		int64(m.Sys / 1024 / 1024),
		int64(m.HeapObjects),
		int64(time.Since(processStartTime).Seconds())
}

// MetricsHandler 暴露 Prometheus 文本格式指标
// 仅 on-demand 计算，hot path 仅 atomic 计数
func MetricsHandler(c *gin.Context) {
	var sb []byte
	add := func(name string, val int64, help, typ string) {
		sb = append(sb, "# HELP "...)
		sb = append(sb, help...)
		sb = append(sb, '\n')
		sb = append(sb, "# TYPE "...)
		sb = append(sb, name...)
		sb = append(sb, ' ')
		sb = append(sb, typ...)
		sb = append(sb, '\n')
		sb = append(sb, name...)
		sb = append(sb, ' ')
		sb = append(sb, strconv.FormatInt(val, 10)...)
		sb = append(sb, '\n')
	}

	m := GlobalMetrics
	add("hubproxy_requests_total", m.RequestsTotal.Load(), "Total processed requests", "counter")
	add("hubproxy_requests_errors_total", m.RequestsErrors.Load(), "Total error responses", "counter")
	add("hubproxy_cache_hits_total", m.CacheHits.Load(), "Cache hits", "counter")
	add("hubproxy_cache_misses_total", m.CacheMisses.Load(), "Cache misses", "counter")
	add("hubproxy_bytes_proxied_total", m.BytesProxied.Load(), "Total bytes proxied", "counter")
	add("hubproxy_docker_manifest_requests_total", m.DockerManifestReqs.Load(), "Docker manifest API calls", "counter")
	add("hubproxy_docker_blob_requests_total", m.DockerBlobReqs.Load(), "Docker blob API calls", "counter")
	add("hubproxy_github_requests_total", m.GitHubReqs.Load(), "GitHub proxy requests", "counter")
	add("hubproxy_search_requests_total", m.SearchReqs.Load(), "Search API requests", "counter")

	// Go 运行时指标
	goroutines, memAllocMB, memSysMB, heapObjects, uptimeSec := runtimeStats()
	add("hubproxy_go_goroutines", goroutines, "Number of goroutines", "gauge")
	add("hubproxy_go_mem_alloc_mb", memAllocMB, "Allocated memory in MB", "gauge")
	add("hubproxy_go_mem_sys_mb", memSysMB, "System memory in MB", "gauge")
	add("hubproxy_go_heap_objects", heapObjects, "Heap objects count", "gauge")
	add("hubproxy_uptime_seconds", uptimeSec, "Process uptime in seconds", "gauge")

	c.Data(200, "text/plain; version=0.0.4; charset=utf-8", sb)
}
