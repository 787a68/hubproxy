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

// AppConfig 应用配置结构体（不可变快照，加载后通过 atomic.Value 发布）
type AppConfig struct {
	Server struct {
		Host           string `toml:"host"`
		Port           int    `toml:"port"`
		FileSize       int64  `toml:"fileSize"`
		EnableH2C      bool   `toml:"enableH2C"`
		EnableFrontend bool   `toml:"enableFrontend"`
	} `toml:"server"`

	RateLimit struct {
		RequestLimit int     `toml:"requestLimit"`
		PeriodHours  float64 `toml:"periodHours"`
	} `toml:"rateLimit"`

	Security struct {
		WhiteList []string `toml:"whiteList"`
		BlackList []string `toml:"blackList"`
	} `toml:"security"`

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
			FileSize:       2 * 1024 * 1024 * 1024,
			EnableH2C:      false,
			EnableFrontend: true,
		},
		RateLimit: struct {
			RequestLimit int     `toml:"requestLimit"`
			PeriodHours  float64 `toml:"periodHours"`
		}{
			RequestLimit: 500,
			PeriodHours:  3.0,
		},
		Security: struct {
			WhiteList []string `toml:"whiteList"`
			BlackList []string `toml:"blackList"`
		}{
			WhiteList: []string{},
			BlackList: []string{},
		},
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
	envBool("ENABLE_H2C", &cfg.Server.EnableH2C)
	envBool("ENABLE_FRONTEND", &cfg.Server.EnableFrontend)
	envInt64("MAX_FILE_SIZE", &cfg.Server.FileSize, 1)
	envInt("RATE_LIMIT", &cfg.RateLimit.RequestLimit, 1)
	envFloat("RATE_PERIOD_HOURS", &cfg.RateLimit.PeriodHours, 0)

	if val := os.Getenv("IP_WHITELIST"); val != "" {
		cfg.Security.WhiteList = append(cfg.Security.WhiteList, strings.Split(val, ",")...)
	}
	if val := os.Getenv("IP_BLACKLIST"); val != "" {
		cfg.Security.BlackList = append(cfg.Security.BlackList, strings.Split(val, ",")...)
	}

	if val, ok := os.LookupEnv("ACCESS_PROXY"); ok {
		cfg.Access.Proxy = strings.TrimSpace(val)
	}
	envInt("MAX_IMAGES", &cfg.Download.MaxImages, 1)
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

func envFloat(key string, dst *float64, minVal float64) {
	if val := os.Getenv(key); val != "" {
		if v, err := strconv.ParseFloat(val, 64); err == nil && v >= minVal {
			*dst = v
		}
	}
}
