package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// RegistryMapping Registry映射配置
type RegistryMapping struct {
	Upstream string `toml:"upstream"`
	AuthHost string `toml:"authHost"`
	AuthType string `toml:"authType"`
	Enabled  bool   `toml:"enabled"`
}

// RateRule 单条限速规则
type RateRule struct {
	PeriodHours  float64
	RequestLimit int
}

// AppConfig 应用配置结构体（不可变快照，加载后通过 atomic.Value 发布）
type AppConfig struct {
	Server struct {
		Host           string `toml:"host"`
		Port           int    `toml:"port"`
		FileSize       int64  `toml:"fileSize"`
		EnableH2C      bool   `toml:"enableH2C"`
		EnableFrontend bool   `toml:"enableFrontend"`
	} `toml:"server"`

	// IPLimits 限速规则列表，格式 "ip periodHours requestLimit"
	// "*" 为全局，其他为按 IP/CIDR 覆盖，requestLimit=0 表示阻断
	IPLimits []string `toml:"ipLimits"`

	Access struct {
		WhiteList []string `toml:"whiteList"`
		BlackList []string `toml:"blackList"`
		Proxy     string   `toml:"proxy"`
	} `toml:"access"`

	Download struct {
		MaxImages int `toml:"maxImages"`
	} `toml:"download"`

	Registries map[string]RegistryMapping `toml:"registries"`

	TokenCache struct {
		Enabled    bool   `toml:"enabled"`
		DefaultTTL string `toml:"defaultTTL"`
	} `toml:"tokenCache"`

	// 解析后的派生字段（不来自 TOML，加载时计算）
	parsedTTL time.Duration
}

// ParsedTTL 返回解析后的默认缓存 TTL
func (c *AppConfig) ParsedTTL() time.Duration { return c.parsedTTL }

// 全局不可变配置快照，通过 atomic.Value 实现无锁读取
var configHolder atomic.Pointer[AppConfig]

// DefaultConfig 返回默认配置
func DefaultConfig() *AppConfig {
	cfg := &AppConfig{
		Server: struct {
			Host           string `toml:"host"`
			Port           int    `toml:"port"`
			FileSize       int64  `toml:"fileSize"`
			EnableH2C      bool   `toml:"enableH2C"`
			EnableFrontend bool   `toml:"enableFrontend"`
		}{
			Host:           "0.0.0.0",
			Port:           5000,
			FileSize:       1 * 1024 * 1024 * 1024,
			EnableH2C:      true,
			EnableFrontend: true,
		},
		IPLimits: []string{"* 1 100"},
		Access: struct {
			WhiteList []string `toml:"whiteList"`
			BlackList []string `toml:"blackList"`
			Proxy     string   `toml:"proxy"`
		}{
			WhiteList: []string{},
			BlackList: []string{},
			Proxy:     "",
		},
		Download: struct {
			MaxImages int `toml:"maxImages"`
		}{
			MaxImages: 10,
		},
		Registries: map[string]RegistryMapping{
			"ghcr.io": {
				Upstream: "ghcr.io",
				AuthHost: "ghcr.io/token",
				AuthType: "github",
				Enabled:  true,
			},
			"gcr.io": {
				Upstream: "gcr.io",
				AuthHost: "gcr.io/v2/token",
				AuthType: "google",
				Enabled:  true,
			},
			"quay.io": {
				Upstream: "quay.io",
				AuthHost: "quay.io/v2/auth",
				AuthType: "quay",
				Enabled:  true,
			},
			"registry.k8s.io": {
				Upstream: "registry.k8s.io",
				AuthHost: "registry.k8s.io",
				AuthType: "anonymous",
				Enabled:  true,
			},
		},
		TokenCache: struct {
			Enabled    bool   `toml:"enabled"`
			DefaultTTL string `toml:"defaultTTL"`
		}{
			Enabled:    true,
			DefaultTTL: "20m",
		},
	}
	cfg.parsedTTL = 20 * time.Minute
	return cfg
}

// GetConfig 无锁获取当前配置快照（hot path，零分配）
func GetConfig() *AppConfig {
	if cfg := configHolder.Load(); cfg != nil {
		return cfg
	}
	cfg := DefaultConfig()
	configHolder.Store(cfg)
	return cfg
}

// setConfig 发布配置快照（启动时一次性调用）
func setConfig(cfg *AppConfig) {
	configHolder.Store(cfg)
}

// configFilePath 返回配置文件路径
func configFilePath() string {
	if path := strings.TrimSpace(os.Getenv("CONFIG_PATH")); path != "" {
		return path
	}
	return "config.toml"
}

// LoadConfig 加载配置（仅在启动时调用一次）
func LoadConfig() error {
	cfg := DefaultConfig()
	path := configFilePath()

	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, cfg); err != nil {
			return fmt.Errorf("解析配置文件 %s 失败: %v", path, err)
		}
	} else {
		log.Printf("未找到配置文件 %s，使用默认配置", path)
	}

	overrideFromEnv(cfg)
	mergeDefaultConfig(cfg)
	applyDerived(cfg)
	setConfig(cfg)
	return nil
}

func mergeDefaultConfig(cfg *AppConfig) {
	defaults := DefaultConfig()
	if cfg.Registries == nil {
		cfg.Registries = defaults.Registries
		return
	}
	for name, mapping := range defaults.Registries {
		if _, ok := cfg.Registries[name]; !ok {
			cfg.Registries[name] = mapping
		}
	}
}

// applyDerived 计算派生字段
func applyDerived(cfg *AppConfig) {
	if cfg.TokenCache.DefaultTTL != "" {
		if parsed, err := time.ParseDuration(cfg.TokenCache.DefaultTTL); err == nil {
			cfg.parsedTTL = parsed
		}
	}
}

// overrideFromEnv 从环境变量覆盖配置
func overrideFromEnv(cfg *AppConfig) {
	if val := os.Getenv("SERVER_HOST"); val != "" {
		cfg.Server.Host = val
	}
	if val := os.Getenv("SERVER_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil && port > 0 {
			cfg.Server.Port = port
		}
	}
	envBool("H2C", &cfg.Server.EnableH2C)
	envBool("FRONTEND", &cfg.Server.EnableFrontend)
	envInt64("FILE_SIZE", &cfg.Server.FileSize, 1)

	if val := os.Getenv("PROXY"); val != "" {
		cfg.Access.Proxy = strings.TrimSpace(val)
	}
	if val := os.Getenv("WHITELIST"); val != "" {
		cfg.Access.WhiteList = strings.Split(val, ",")
	}
	if val := os.Getenv("BLACKLIST"); val != "" {
		cfg.Access.BlackList = strings.Split(val, ",")
	}
	envInt("MAX_IMAGES", &cfg.Download.MaxImages, 1)

	envBool("CACHE", &cfg.TokenCache.Enabled)
	if val := os.Getenv("CACHE_TTL"); val != "" {
		cfg.TokenCache.DefaultTTL = val
	}

	if val := os.Getenv("IP_LIMITS"); val != "" {
		cfg.IPLimits = strings.Split(val, ",")
	}
}

// ParseIPLimit 解析单条限速规则 "ip periodHours requestLimit"
func ParseIPLimit(rule string) (ipSpec string, r RateRule, ok bool) {
	fields := strings.Fields(rule)
	if len(fields) != 3 {
		return "", RateRule{}, false
	}
	period, err1 := strconv.ParseFloat(fields[1], 64)
	limit, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil {
		return "", RateRule{}, false
	}
	return fields[0], RateRule{PeriodHours: period, RequestLimit: limit}, true
}

func envBool(key string, dst *bool) {
	if val := os.Getenv(key); val != "" {
		if enable, err := strconv.ParseBool(val); err == nil {
			*dst = enable
		}
	}
}

func envInt(key string, dst *int, minVal int) {
	if val := os.Getenv(key); val != "" {
		if v, err := strconv.Atoi(val); err == nil && v >= minVal {
			*dst = v
		}
	}
}

func envInt64(key string, dst *int64, minVal int64) {
	if val := os.Getenv(key); val != "" {
		if v, err := strconv.ParseInt(val, 10, 64); err == nil && v >= minVal {
			*dst = v
		}
	}
}
