package utils

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
)

// CachedItem 通用缓存项
type CachedItem struct {
	Data        []byte
	ContentType string
	Headers     map[string]string
	ExpiresAt   time.Time
}

const (
	cacheShards      = 64
	cacheMaxPerShard = 512
)

// cacheShard 分片缓存，每个分片独立互斥锁
type cacheShard struct {
	mu    sync.Mutex
	items map[string]*CachedItem
}

// UniversalCache 分片 + 大小限制的缓存
type UniversalCache struct {
	shards [cacheShards]cacheShard
}

// GlobalCache 全局缓存实例
var GlobalCache = &UniversalCache{}

func (c *UniversalCache) shard(key string) *cacheShard {
	return &c.shards[fnv32(key)%cacheShards]
}

// Get 获取缓存项（hot path，仅持锁一次分片）
func (c *UniversalCache) Get(key string) *CachedItem {
	s := c.shard(key)
	s.mu.Lock()
	item, ok := s.items[key]
	if ok && time.Now().After(item.ExpiresAt) {
		delete(s.items, key)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return item
}

// Set 写入缓存，超额时随机淘汰
func (c *UniversalCache) Set(key string, data []byte, contentType string, headers map[string]string, ttl time.Duration) {
	s := c.shard(key)
	s.mu.Lock()
	if len(s.items) >= cacheMaxPerShard {
		for k := range s.items {
			delete(s.items, k)
			break
		}
	}
	s.items[key] = &CachedItem{
		Data:        data,
		ContentType: contentType,
		Headers:     headers,
		ExpiresAt:   time.Now().Add(ttl),
	}
	s.mu.Unlock()
}

func (c *UniversalCache) GetToken(key string) string {
	if item := c.Get(key); item != nil {
		return string(item.Data)
	}
	return ""
}

func (c *UniversalCache) SetToken(key, token string, ttl time.Duration) {
	c.Set(key, []byte(token), "application/json", nil, ttl)
}

// fnv32 FNV-1a 32位哈希（比 MD5 快 100 倍，用于分片选择）
func fnv32(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// BuildCacheKey 构建稳定的缓存 key
func BuildCacheKey(prefix, query string) string {
	return fmt.Sprintf("%s:%x", prefix, md5.Sum([]byte(query)))
}

func BuildTokenCacheKey(query string) string {
	return BuildCacheKey("token", query)
}

func BuildManifestCacheKey(imageRef, reference string) string {
	key := fmt.Sprintf("%s:%s", imageRef, reference)
	return BuildCacheKey("manifest", key)
}

// defaultTTL 返回配置的默认缓存 TTL，无效时回退到 30 分钟
func defaultTTL() time.Duration {
	if t := config.GetConfig().ParsedTTL(); t > 0 {
		return t
	}
	return 30 * time.Minute
}

// GetManifestTTL 根据引用类型决定缓存 TTL
func GetManifestTTL(reference string) time.Duration {
	if strings.HasPrefix(reference, "sha256:") {
		return 24 * time.Hour
	}
	// 可变标签用短 TTL，避免拉到旧 manifest
	switch reference {
	case "latest", "main", "master", "dev", "develop":
		return 10 * time.Minute
	}
	return defaultTTL()
}

// ExtractTTLFromResponse 从响应中智能提取 TTL
func ExtractTTLFromResponse(responseBody []byte) time.Duration {
	var tokenResp struct {
		ExpiresIn int `json:"expires_in"`
	}
	if json.Unmarshal(responseBody, &tokenResp) == nil && tokenResp.ExpiresIn > 0 {
		safeTTL := time.Duration(tokenResp.ExpiresIn-300) * time.Second
		if safeTTL > 5*time.Minute {
			return safeTTL
		}
	}
	return defaultTTL()
}

func WriteTokenResponse(c *gin.Context, cachedBody string) {
	c.Header("Content-Type", "application/json")
	c.String(200, cachedBody)
}

func WriteCachedResponse(c *gin.Context, item *CachedItem) {
	if item.ContentType != "" {
		c.Header("Content-Type", item.ContentType)
	}
	for key, value := range item.Headers {
		c.Header(key, value)
	}
	c.Data(200, item.ContentType, item.Data)
}

// IsCacheEnabled 检查缓存是否启用
func IsCacheEnabled() bool {
	return config.GetConfig().CacheEnabled()
}

// init 启动时初始化所有分片 map，并启动后台过期清理
func init() {
	for i := range GlobalCache.shards {
		GlobalCache.shards[i].items = make(map[string]*CachedItem, cacheMaxPerShard)
	}

	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			for i := range GlobalCache.shards {
				s := &GlobalCache.shards[i]
				s.mu.Lock()
				for k, v := range s.items {
					if now.After(v.ExpiresAt) {
						delete(s.items, k)
					}
				}
				s.mu.Unlock()
			}
		}
	}()
}
