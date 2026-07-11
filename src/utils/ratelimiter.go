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
	// ipCleanupInterval IP 限流器过期条目清理周期
	ipCleanupInterval = 20 * time.Minute
	maxIPCacheSize     = 10000
	ipShardCount       = 64
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
	shards       [ipShardCount]ipShard
	defaultLimit rate.Limit
	defaultBurst int
	defaultBlock bool
	rules        []ipRule
}

type rateLimiterEntry struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// InitGlobalLimiter 初始化全局限流器
func InitGlobalLimiter() *IPRateLimiter {
	cfg := config.GetConfig()

	limiter := &IPRateLimiter{}

	// 解析限速规则
	for _, ruleStr := range cfg.IPLimits {
		ipSpec, rule, ok := config.ParseIPLimit(ruleStr)
		if !ok {
			Logger().Warn("invalid IP limit rule", "rule", ruleStr)
			continue
		}

		if ipSpec == "*" {
			// 全局规则
			if rule.RequestLimit <= 0 {
				limiter.defaultBlock = true
			} else {
				limiter.defaultLimit = rate.Limit(float64(rule.RequestLimit) / (rule.PeriodHours * 3600))
				limiter.defaultBurst = rule.RequestLimit
			}
			continue
		}

		// 按 IP/CIDR 覆盖规则
		r := ipRule{}
		if rule.RequestLimit <= 0 {
			r.blocked = true
		} else {
			r.limit = rate.Limit(float64(rule.RequestLimit) / (rule.PeriodHours * 3600))
			r.burst = rule.RequestLimit
		}
		if _, ipnet, err := net.ParseCIDR(ipSpec); err == nil {
			r.cidr = ipnet
		} else if ip := net.ParseIP(ipSpec); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				r.cidr = &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
			} else {
				r.cidr = &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
			}
		} else {
			Logger().Warn("invalid IP limit rule", "ip", ipSpec)
			continue
		}
		limiter.rules = append(limiter.rules, r)
	}

	for i := range limiter.shards {
		limiter.shards[i].entries = make(map[string]*rateLimiterEntry, 64)
	}

	go limiter.cleanupRoutine()

	return limiter
}

// cleanupRoutine 定期清理过期条目
func (i *IPRateLimiter) cleanupRoutine() {
	ticker := time.NewTicker(ipCleanupInterval)
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

// matchRule 检查 IP 是否匹配覆盖规则
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

	// 优先匹配 IP/CIDR 覆盖规则，否则用全局规则
	r, b := i.defaultLimit, i.defaultBurst
	blocked := i.defaultBlock
	if limit, burst, bl, matched := i.matchRule(ip); matched {
		r, b, blocked = limit, burst, bl
	}

	if blocked {
		shard.entries[normalized] = &rateLimiterEntry{limiter: nil, lastAccess: now}
		shard.mu.Unlock()
		return nil
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

		ip := c.ClientIP()

		ipLimiter := limiter.GetLimiter(ip)
		if ipLimiter == nil {
			c.Header("Retry-After", "3600")
			c.JSON(http.StatusForbidden, gin.H{"error": "您已被限制访问"})
			c.Abort()
			return
		}
		if !ipLimiter.Allow() {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求频率过快，暂时限制访问"})
			c.Abort()
			return
		}
		c.Next()
	}
}
