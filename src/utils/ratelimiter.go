package utils

import (
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

// ipRule 单条 IP 限速规则
type ipRule struct {
	cidr    *net.IPNet
	limit   rate.Limit
	burst   int
	blocked bool
}

// ipShard 分片限流器，独立锁降低争用
type ipShard struct {
	mu      sync.Mutex
	entries map[string]*rateLimiterEntry
}

// IPRateLimiter 分片 IP 限流器
type IPRateLimiter struct {
	shards [ipShardCount]ipShard
	r      rate.Limit
	b      int
	rules  []ipRule
}

type rateLimiterEntry struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// InitGlobalLimiter 初始化全局限流器
func InitGlobalLimiter() *IPRateLimiter {
	cfg := config.GetConfig()

	limiter := &IPRateLimiter{
		r: rate.Limit(float64(cfg.RateLimit.RequestLimit) / (cfg.RateLimit.PeriodHours * 3600)),
		b: cfg.RateLimit.RequestLimit,
	}

	// 解析 IPLimits 规则
	for ipSpec, limit := range cfg.IPLimits {
		rule := ipRule{}
		if limit <= 0 {
			rule.blocked = true
		} else {
			rule.limit = rate.Limit(float64(limit) / (cfg.RateLimit.PeriodHours * 3600))
			rule.burst = limit
		}
		if _, ipnet, err := net.ParseCIDR(ipSpec); err == nil {
			rule.cidr = ipnet
		} else if ip := net.ParseIP(ipSpec); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				rule.cidr = &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
			} else {
				rule.cidr = &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
			}
		} else {
			Logger().Warn("invalid IP limit rule", "ip", ipSpec)
			continue
		}
		limiter.rules = append(limiter.rules, rule)
	}

	for i := range limiter.shards {
		limiter.shards[i].entries = make(map[string]*rateLimiterEntry, 64)
	}

	go limiter.cleanupRoutine()

	return limiter
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
func normalizeIPv6(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	if v4 := ip.To4(); v4 != nil {
		return ipStr
	}
	buf := make(net.IP, net.IPv6len)
	copy(buf, ip.To16())
	for j := 8; j < 16; j++ {
		buf[j] = 0
	}
	return buf.String()
}

// shard 计算分片索引
func (i *IPRateLimiter) shardFor(ip string) *ipShard {
	return &i.shards[fnv32(ip)%ipShardCount]
}

// matchRule 检查 IP 是否匹配自定义规则，返回 (limit, burst, blocked, matched)
func (i *IPRateLimiter) matchRule(ip string) (rate.Limit, int, bool, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 0, 0, false, false
	}
	for _, rule := range i.rules {
		if rule.cidr != nil && rule.cidr.Contains(parsed) {
			return rule.limit, rule.burst, rule.blocked, true
		}
	}
	return 0, 0, false, false
}

// GetLimiter 获取指定 IP 的限流器，返回 nil 表示阻断
func (i *IPRateLimiter) GetLimiter(ip string) *rate.Limiter {
	normalized := normalizeIPv6(ip)
	shard := i.shardFor(normalized)
	now := time.Now()

	shard.mu.Lock()
	entry, ok := shard.entries[normalized]
	if ok {
		entry.lastAccess = now
		shard.mu.Unlock()
		return entry.limiter
	}

	// 首次访问时确定限流参数
	r, b := i.r, i.b
	if limit, burst, blocked, matched := i.matchRule(ip); matched {
		if blocked {
			shard.entries[normalized] = &rateLimiterEntry{limiter: nil, lastAccess: now}
			shard.mu.Unlock()
			return nil
		}
		r, b = limit, burst
	}

	entry = &rateLimiterEntry{
		limiter:    rate.NewLimiter(r, b),
		lastAccess: now,
	}
	shard.entries[normalized] = entry
	shard.mu.Unlock()
	return entry.limiter
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

		ipLimiter := limiter.GetLimiter(ip)
		if ipLimiter == nil {
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
