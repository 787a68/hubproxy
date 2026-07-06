package utils

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"hubproxy/config"
)

const (
	cleanupInterval = 20 * time.Minute
	maxIPCacheSize  = 10000
	ipShardCount    = 64
)

// ipShard 分片限流器，独立锁降低争用
type ipShard struct {
	mu      sync.Mutex
	entries map[string]*rateLimiterEntry
}

// IPRateLimiter 分片 IP 限流器
type IPRateLimiter struct {
	shards           [ipShardCount]ipShard
	r                rate.Limit
	b                int
	whitelist        []*net.IPNet
	blacklist        []*net.IPNet
	whitelistLimiter *rate.Limiter
}

type rateLimiterEntry struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// InitGlobalLimiter 初始化全局限流器
func InitGlobalLimiter() *IPRateLimiter {
	cfg := config.GetConfig()

	limiter := &IPRateLimiter{
		r:                rate.Limit(float64(cfg.RateLimit.RequestLimit) / (cfg.RateLimit.PeriodHours * 3600)),
		b:                cfg.RateLimit.RequestLimit,
		whitelistLimiter: rate.NewLimiter(rate.Inf, cfg.RateLimit.RequestLimit),
	}

	limiter.whitelist = parseCIDRList(cfg.Security.WhiteList, "白名单")
	limiter.blacklist = parseCIDRList(cfg.Security.BlackList, "黑名单")

	for i := range limiter.shards {
		limiter.shards[i].entries = make(map[string]*rateLimiterEntry, 64)
	}

	go limiter.cleanupRoutine()

	return limiter
}

// parseCIDRList 解析 CIDR/IP 列表
func parseCIDRList(items []string, name string) []*net.IPNet {
	result := make([]*net.IPNet, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			if !strings.Contains(item, "/") {
				if strings.Contains(item, ":") {
					item += "/128"
				} else {
					item += "/32"
				}
			}
			if _, ipnet, err := net.ParseCIDR(item); err == nil {
				result = append(result, ipnet)
			} else {
				log.Printf("警告: 无效的%sIP格式: %s", name, item)
			}
		}
	}
	return result
}

// cleanupRoutine 定期清理过期条目
func (i *IPRateLimiter) cleanupRoutine() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		for s := range i.shards {
			shard := &i.shards[s]
			shard.mu.Lock()
			for ip, entry := range shard.entries {
				if now.Sub(entry.lastAccess) > 2*time.Hour {
					delete(shard.entries, ip)
				}
			}
			// 超过上限时清空整个分片（避免单分片无限增长）
			if len(shard.entries) > maxIPCacheSize/ipShardCount {
				shard.entries = make(map[string]*rateLimiterEntry, 64)
			}
			shard.mu.Unlock()
		}
	}
}

// extractIP 从地址中提取纯 IP
func extractIP(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// normalizeIPv6 将 IPv6 的接口标识符清零，归并到 /64 段
// 返回独立副本，避免修改原始 IP 字节
func normalizeIPv6(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	// IPv4 直接返回
	if v4 := ip.To4(); v4 != nil {
		return ipStr
	}
	// IPv6：复制一份，避免修改 ParseIP 返回的底层数组
	buf := make(net.IP, net.IPv6len)
	copy(buf, ip.To16())
	for j := 8; j < 16; j++ {
		buf[j] = 0
	}
	return buf.String()
}

// isIPInCIDRList 检查 IP 是否在 CIDR 列表中
func isIPInCIDRList(ip string, cidrList []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range cidrList {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// shard 计算分片索引
func (i *IPRateLimiter) shardFor(ip string) *ipShard {
	s := &i.shards[fnv32(ip)%ipShardCount]
	if s.entries == nil {
		s.mu.Lock()
		if s.entries == nil {
			s.entries = make(map[string]*rateLimiterEntry, 64)
		}
		s.mu.Unlock()
	}
	return s
}

// GetLimiter 获取指定 IP 的限流器
func (i *IPRateLimiter) GetLimiter(ip string) (*rate.Limiter, bool) {
	if isIPInCIDRList(ip, i.blacklist) {
		return nil, false
	}
	if isIPInCIDRList(ip, i.whitelist) {
		return i.whitelistLimiter, true
	}

	normalized := normalizeIPv6(ip)
	shard := i.shardFor(normalized)
	now := time.Now()

	shard.mu.Lock()
	entry, ok := shard.entries[normalized]
	if ok {
		entry.lastAccess = now
		shard.mu.Unlock()
		return entry.limiter, true
	}

	entry = &rateLimiterEntry{
		limiter:    rate.NewLimiter(i.r, i.b),
		lastAccess: now,
	}
	shard.entries[normalized] = entry
	shard.mu.Unlock()
	return entry.limiter, true
}

// RateLimitMiddleware 速率限制中间件
func RateLimitMiddleware(limiter *IPRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 前端静态资源不限流
		path := c.Request.URL.Path
		if path == "/" || path == "/favicon.ico" || path == "/images.html" ||
			path == "/search.html" || strings.HasPrefix(path, "/public/") {
			c.Next()
			return
		}

		var ip string
		if forwarded := c.GetHeader("X-Forwarded-For"); forwarded != "" {
			ips := strings.Split(forwarded, ",")
			ip = strings.TrimSpace(ips[0])
		} else if realIP := c.GetHeader("X-Real-IP"); realIP != "" {
			ip = realIP
		} else if remoteIP := c.GetHeader("X-Original-Forwarded-For"); remoteIP != "" {
			ips := strings.Split(remoteIP, ",")
			ip = strings.TrimSpace(ips[0])
		} else {
			ip = c.ClientIP()
		}
		ip = extractIP(ip)

		ipLimiter, allowed := limiter.GetLimiter(ip)
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": "您已被限制访问"})
			c.Abort()
			return
		}
		if !ipLimiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求频率过快，暂时限制访问"})
			c.Abort()
			return
		}
		c.Next()
	}
}