package utils

import (
	"net"
	"net/http"
	"net/url"
	"time"

	"hubproxy/config"
)

var (
	globalHTTPClient *http.Client
	searchHTTPClient *http.Client
)

// InitHTTPClients 初始化全局 HTTP 客户端（启动时一次性调用）
// 相比原版：移除 os.Setenv 副作用，直接构造 ProxyURL
func InitHTTPClients() {
	cfg := config.GetConfig()

	proxyFunc := http.ProxyFromEnvironment
	if p := cfg.Access.Proxy; p != "" {
		proxyURL, err := url.Parse(p)
		if err != nil {
			Logger().Warn("parse proxy URL failed", "proxy", p, "err", err)
		} else {
			proxyFunc = http.ProxyURL(proxyURL)
		}
	}

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	globalTransport := &http.Transport{
		Proxy:                 proxyFunc,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second,
		ForceAttemptHTTP2:     true,
		ReadBufferSize:        64 * 1024,
		WriteBufferSize:       64 * 1024,
	}

	searchTransport := &http.Transport{
		Proxy: proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	globalHTTPClient = &http.Client{Transport: globalTransport}
	searchHTTPClient = &http.Client{
		Timeout:   10 * time.Second,
		Transport: searchTransport,
	}
}

// GetGlobalHTTPClient 返回全局 HTTP 客户端
func GetGlobalHTTPClient() *http.Client { return globalHTTPClient }

// GetSearchHTTPClient 返回搜索专用 HTTP 客户端
func GetSearchHTTPClient() *http.Client { return searchHTTPClient }
