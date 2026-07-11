package handlers

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
	"hubproxy/utils"
)

var (
	// githubExps GitHub/HuggingFace 等加速 URL 匹配正则
	// githubExpIndex 记录每个正则的语义索引，用于解耦数组顺序与逻辑判断
	githubExps = []*regexp.Regexp{
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:releases|archive)/.*`),
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:blob|raw)/.*`),
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:info|git-).*`),
		regexp.MustCompile(`^(?:https?://)?raw\.github(?:usercontent|)\.com/([^/]+)/([^/]+)/.+?/.+`),
		regexp.MustCompile(`^(?:https?://)?gist\.(?:githubusercontent|github)\.com/([^/]+)/([^/]+).*`),
		regexp.MustCompile(`^(?:https?://)?api\.github\.com/repos/([^/]+)/([^/]+)/.*`),
		regexp.MustCompile(`^(?:https?://)?huggingface\.co(?:/spaces)?/([^/]+)/(.+)`),
		regexp.MustCompile(`^(?:https?://)?cdn-lfs\.hf\.co(?:/spaces)?/([^/]+)/([^/]+)(?:/(.*))?`),
		regexp.MustCompile(`^(?:https?://)?download\.docker\.com/([^/]+)/.*\.(tgz|zip)`),
		regexp.MustCompile(`^(?:https?://)?(github|opengraph)\.githubassets\.com/([^/]+)/.+?`),
	}

	// githubExpBlobRaw 是 blob/raw 转换对应的正则索引
	githubExpBlobRaw = 1
)

// 全局变量：被阻止的内容类型
var blockedContentTypes = map[string]bool{
	"text/html":             true,
	"application/xhtml+xml": true,
	"text/xml":              true,
	"application/xml":       true,
}

// GitHubProxyHandler GitHub代理处理器
func GitHubProxyHandler(c *gin.Context) {
	utils.GlobalMetrics.GitHubReqs.Add(1)
	rawPath := strings.TrimPrefix(c.Request.URL.RequestURI(), "/")
	for strings.HasPrefix(rawPath, "/") {
		rawPath = strings.TrimPrefix(rawPath, "/")
	}

	// 自动补全协议头
	if !strings.HasPrefix(rawPath, "https://") {
		if strings.HasPrefix(rawPath, "http://") {
			rawPath = strings.TrimPrefix(rawPath, "http://")
		} else if strings.HasPrefix(rawPath, "https:/") && !strings.HasPrefix(rawPath, "https://") {
			rawPath = strings.TrimPrefix(rawPath, "https:/")
		} else if strings.HasPrefix(rawPath, "http:/") && !strings.HasPrefix(rawPath, "http://") {
			rawPath = strings.TrimPrefix(rawPath, "http:/")
		}
		rawPath = "https://" + rawPath
	}

	matchedIdx, matches := CheckGitHubURL(rawPath)
	if matches != nil {
		if allowed, reason := utils.GlobalAccessController.CheckGitHubAccess(matches); !allowed {
			var repoPath string
			if len(matches) >= 2 {
				username := matches[0]
				repoName := strings.TrimSuffix(matches[1], ".git")
				repoPath = username + "/" + repoName
			}
			utils.Logger().Warn("github access denied", "repo", repoPath, "reason", reason)
			c.String(http.StatusForbidden, reason)
			return
		}
	} else {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	// 将 blob 链接转换为 raw 链接（复用已匹配的索引，避免二次正则匹配）
	if matchedIdx == githubExpBlobRaw {
		rawPath = strings.Replace(rawPath, "/blob/", "/raw/", 1)
	}

	proxyGitHubRequest(c, rawPath)
}

// CheckGitHubURL 检查URL是否匹配GitHub模式，返回匹配的索引和捕获组
func CheckGitHubURL(u string) (int, []string) {
	for i, exp := range githubExps {
		if matches := exp.FindStringSubmatch(u); matches != nil {
			return i, matches[1:]
		}
	}
	return -1, nil
}

// proxyGitHubRequest 代理GitHub请求
func proxyGitHubRequest(c *gin.Context, u string) {
	proxyGitHubWithRedirect(c, u, 0)
}

// proxyGitHubWithRedirect 带重定向的GitHub代理请求
func proxyGitHubWithRedirect(c *gin.Context, u string, redirectCount int) {
	const maxRedirects = 20
	if redirectCount > maxRedirects {
		c.String(http.StatusLoopDetected, "重定向次数过多，可能存在循环重定向")
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, u, c.Request.Body)
	if err != nil {
		utils.Logger().Warn("create request failed", "url", u, "err", err)
		c.String(http.StatusInternalServerError, "server error")
		return
	}

	// 复制请求头，跳过逐跳/传输类头部，避免重定向后长度或编码不匹配
	for key, values := range c.Request.Header {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Del("Host")
	req.Header.Set("Accept-Language", "en-US")

	resp, err := utils.GetGlobalHTTPClient().Do(req)
	if err != nil {
		utils.Logger().Warn("upstream request failed", "url", u, "err", err)
		c.String(http.StatusInternalServerError, "server error")
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			utils.Logger().Warn("close response body failed", "err", err)
		}
	}()

	// 检查并处理被阻止的内容类型
	if c.Request.Method == http.MethodGet {
		if contentType := resp.Header.Get("Content-Type"); blockedContentTypes[strings.ToLower(strings.Split(contentType, ";")[0])] {
			c.JSON(http.StatusForbidden, map[string]string{
				"error":   "Content type not allowed",
				"message": "检测到网页类型，本服务不支持加速网页，请检查您的链接是否正确。",
			})
			return
		}
	}

	// 检查文件大小限制
	cfg := config.GetConfig()
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		if size, err := strconv.ParseInt(contentLength, 10, 64); err == nil && size > cfg.Server.FileSize {
			c.String(http.StatusRequestEntityTooLarge,
				"文件过大，限制大小: %d MB", cfg.Server.FileSize/(1024*1024))
			return
		}
	}

	// 清理安全相关的头
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Referrer-Policy")
	resp.Header.Del("Strict-Transport-Security")

	// 获取真实域名
	realHost := c.Request.Header.Get("X-Forwarded-Host")
	if realHost == "" {
		realHost = c.Request.Host
	}
	if !strings.HasPrefix(realHost, "http://") && !strings.HasPrefix(realHost, "https://") {
		realHost = "https://" + realHost
	}

	// 处理重定向（在写入响应体之前统一处理，避免 .sh/.ps1 分支丢弃已处理内容）
	if location := resp.Header.Get("Location"); location != "" {
		if _, m := CheckGitHubURL(location); m != nil {
			// 可识别的 GitHub/HF 重定向，改写为代理相对路径返回给客户端
			for k, vs := range resp.Header {
				if isHopByHopHeader(k) {
					continue
				}
				for _, v := range vs {
					c.Header(k, v)
				}
			}
			c.Header("Location", "/"+location)
			c.Status(resp.StatusCode)
			return
		}
		// 其他重定向：递归跟随上游（不返回给客户端）
		proxyGitHubWithRedirect(c, location, redirectCount+1)
		return
	}

	// 复制响应头（跳过逐跳头部）
	for key, values := range resp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			c.Header(key, value)
		}
	}

	// 处理.sh和.ps1文件的智能处理（URL 后缀判断）
	lowerURL := strings.ToLower(u)
	if strings.HasSuffix(lowerURL, ".sh") || strings.HasSuffix(lowerURL, ".ps1") {
		isGzipCompressed := resp.Header.Get("Content-Encoding") == "gzip"

		processedBody, _, err := utils.ProcessSmart(resp.Body, isGzipCompressed, realHost)
		if err != nil {
			utils.Logger().Warn("script processing failed", "url", u, "err", err)
			c.String(http.StatusBadGateway, "Script processing failed")
			return
		}

		// 处理后内容长度可能变化，移除原始 Content-Length/Encoding
		c.Header("Content-Length", "")
		c.Header("Content-Encoding", "")
		c.Status(resp.StatusCode)

		if _, err := io.Copy(c.Writer, processedBody); err != nil {
			utils.Logger().Warn("write processed script body failed", "url", u, "err", err)
		}
		return
	}

	c.Status(resp.StatusCode)

	// 直接流式转发
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		utils.Logger().Warn("forward response body failed", "url", u, "err", err)
	}
}
