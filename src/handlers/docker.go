package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"hubproxy/config"
	"hubproxy/utils"
)

// ProxyDockerRegistryGin /v2/* 路由入口
func ProxyDockerRegistryGin(c *gin.Context) {
	path := c.Request.URL.Path
	if path == "/v2/" {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	if !strings.HasPrefix(path, "/v2/") {
		c.String(http.StatusNotFound, "Docker Registry API v2 only")
		return
	}
	handleRegistryRequest(c, strings.TrimPrefix(path, "/v2/"))
}

// handleRegistryRequest 路由到默认 Docker Hub 或上游 Registry
func handleRegistryRequest(c *gin.Context, pathWithoutV2 string) {
	imageName, apiType, reference := parseRegistryPath(pathWithoutV2)
	if imageName == "" || apiType == "" {
		c.String(http.StatusBadRequest, "Invalid path format")
		return
	}

	var imageRef string
	var options []remote.Option

	if domain, remaining := detectRegistryDomain(pathWithoutV2); domain != "" {
		if mapping, ok := getRegistryMapping(domain); ok {
			c.Set("target_registry_domain", domain)
			imgName, _, _ := parseRegistryPath(remaining)
			if !checkDockerAccess(c, domain+"/"+imgName) {
				return
			}
			imageRef = fmt.Sprintf("%s/%s", mapping.Upstream, imgName)
			options = createUpstreamOptions(mapping)
		}
		if options == nil {
			c.String(http.StatusBadRequest, "Registry not configured")
			return
		}
	} else {
		// 默认 Docker Hub
		if !strings.Contains(imageName, "/") {
			imageName = "library/" + imageName
		}
		if !checkDockerAccess(c, imageName) {
			return
		}
		imageRef = fmt.Sprintf("%s/%s", dockerHubRegistry, imageName)
		options = dockerHubOptions
	}

	switch apiType {
	case "manifests":
		utils.GlobalMetrics.DockerManifestReqs.Add(1)
		handleManifestRequest(c, imageRef, reference, options)
	case "blobs":
		utils.GlobalMetrics.DockerBlobReqs.Add(1)
		handleBlobRequest(c, imageRef, reference, options)
	case "tags":
		handleTagsRequest(c, imageRef, options)
	default:
		c.String(http.StatusNotFound, "API endpoint not found")
	}
}

// checkDockerAccess 检查 Docker 镜像访问权限
func checkDockerAccess(c *gin.Context, image string) bool {
	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(image); !allowed {
		utils.Logger().Warn("docker access denied", "image", image, "reason", reason)
		c.String(http.StatusForbidden, "镜像访问被限制")
		return false
	}
	return true
}

// parseRegistryPath 解析 Registry 路径
func parseRegistryPath(path string) (imageName, apiType, reference string) {
	if idx := strings.Index(path, "/manifests/"); idx != -1 {
		return path[:idx], "manifests", path[idx+len("/manifests/"):]
	}
	if idx := strings.Index(path, "/blobs/"); idx != -1 {
		return path[:idx], "blobs", path[idx+len("/blobs/"):]
	}
	if idx := strings.Index(path, "/tags/list"); idx != -1 {
		return path[:idx], "tags", "list"
	}
	return "", "", ""
}

// handleManifestRequest 处理 manifest 请求（HEAD/GET，含缓存）
func handleManifestRequest(c *gin.Context, imageRef, reference string, options []remote.Option) {
	if utils.IsCacheEnabled() && c.Request.Method == http.MethodGet {
		cacheKey := utils.BuildManifestCacheKey(imageRef, reference)
		if cached := utils.GlobalCache.Get(cacheKey); cached != nil {
			utils.GlobalMetrics.CacheHits.Add(1)
			utils.WriteCachedResponse(c, cached)
			return
		}
		utils.GlobalMetrics.CacheMisses.Add(1)
	}

	ref, err := parseReference(imageRef, reference)
	if err != nil {
		utils.Logger().Warn("parse reference failed", "ref", imageRef, "err", err)
		c.String(http.StatusBadRequest, "Invalid reference")
		return
	}

	if c.Request.Method == http.MethodHead {
		desc, err := remote.Head(ref, options...)
		if err != nil {
			utils.Logger().Warn("head manifest failed", "ref", imageRef, "err", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}
		c.Header("Content-Type", string(desc.MediaType))
		c.Header("Docker-Content-Digest", desc.Digest.String())
		c.Header("Content-Length", fmt.Sprintf("%d", desc.Size))
		c.Status(http.StatusOK)
		return
	}

	desc, err := remote.Get(ref, options...)
	if err != nil {
		utils.Logger().Warn("get manifest failed", "ref", imageRef, "err", err)
		c.String(http.StatusNotFound, "Manifest not found")
		return
	}

	headers := map[string]string{
		"Docker-Content-Digest": desc.Digest.String(),
		"Content-Length":        fmt.Sprintf("%d", len(desc.Manifest)),
	}

	if utils.IsCacheEnabled() {
		utils.GlobalCache.Set(utils.BuildManifestCacheKey(imageRef, reference),
			desc.Manifest, string(desc.MediaType), headers, utils.GetManifestTTL(reference))
	}

	c.Header("Content-Type", string(desc.MediaType))
	for k, v := range headers {
		c.Header(k, v)
	}
	c.Data(http.StatusOK, string(desc.MediaType), desc.Manifest)
}

// handleBlobRequest 处理 blob 请求（流式传输）
func handleBlobRequest(c *gin.Context, imageRef, digest string, options []remote.Option) {
	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", imageRef, digest))
	if err != nil {
		utils.Logger().Warn("parse digest failed", "ref", imageRef, "err", err)
		c.String(http.StatusBadRequest, "Invalid digest reference")
		return
	}

	layer, err := remote.Layer(digestRef, options...)
	if err != nil {
		utils.Logger().Warn("get layer failed", "ref", imageRef, "digest", digest, "err", err)
		c.String(http.StatusNotFound, "Layer not found")
		return
	}

	size, err := layer.Size()
	if err != nil {
		utils.Logger().Warn("get layer size failed", "ref", imageRef, "err", err)
		c.String(http.StatusInternalServerError, "Failed to get layer size")
		return
	}

	reader, err := layer.Compressed()
	if err != nil {
		utils.Logger().Warn("get layer content failed", "ref", imageRef, "err", err)
		c.String(http.StatusInternalServerError, "Failed to get layer content")
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	c.Header("Docker-Content-Digest", digest)
	c.Status(http.StatusOK)

	written, err := io.Copy(c.Writer, reader)
	if err != nil {
		utils.Logger().Warn("copy layer failed", "ref", imageRef, "err", err)
	}
	utils.GlobalMetrics.BytesProxied.Add(written)
}

// handleTagsRequest 处理 tags 列表请求
func handleTagsRequest(c *gin.Context, imageRef string, options []remote.Option) {
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		utils.Logger().Warn("parse repository failed", "ref", imageRef, "err", err)
		c.String(http.StatusBadRequest, "Invalid repository")
		return
	}

	tags, err := remote.List(repo, options...)
	if err != nil {
		utils.Logger().Warn("list tags failed", "ref", imageRef, "err", err)
		c.String(http.StatusNotFound, "Tags not found")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name": strings.TrimPrefix(imageRef, repo.Registry.Name()+"/"),
		"tags": tags,
	})
}

// parseReference 解析镜像引用（digest 或 tag）
func parseReference(imageRef, reference string) (name.Reference, error) {
	if strings.HasPrefix(reference, "sha256:") {
		return name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	}
	return name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
}

// ===== Docker Hub 默认代理 =====

const dockerHubRegistry = "registry-1.docker.io"

var dockerHubOptions []remote.Option

func InitDockerProxy() {
	dockerHubOptions = []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
	}
}

// ===== 多 Registry 检测 =====

// detectRegistryDomain 检测 Registry 域名
func detectRegistryDomain(path string) (string, string) {
	cfg := config.GetConfig()
	for domain := range cfg.Registries {
		if strings.HasPrefix(path, domain+"/") {
			return domain, strings.TrimPrefix(path, domain+"/")
		}
	}
	return "", path
}

// getRegistryMapping 获取 Registry 映射
func getRegistryMapping(domain string) (config.RegistryMapping, bool) {
	cfg := config.GetConfig()
	m, ok := cfg.Registries[domain]
	return m, ok && m.Enabled
}

// createUpstreamOptions 创建上游请求选项
func createUpstreamOptions(mapping config.RegistryMapping) []remote.Option {
	return []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
	}
}

// ===== 认证代理 =====

// ProxyDockerAuthGin Docker 认证代理
func ProxyDockerAuthGin(c *gin.Context) {
	if utils.IsCacheEnabled() {
		proxyDockerAuthWithCache(c)
	} else {
		proxyDockerAuthOriginalWithDomain(c, detectAuthTargetDomain(c))
	}
}

func proxyDockerAuthWithCache(c *gin.Context) {
	targetDomain := detectAuthTargetDomain(c)
	cacheKey := utils.BuildTokenCacheKey(targetDomain + ":" + c.Request.URL.RawQuery)
	if cached := utils.GlobalCache.GetToken(cacheKey); cached != "" {
		utils.GlobalMetrics.CacheHits.Add(1)
		utils.WriteTokenResponse(c, cached)
		return
	}
	utils.GlobalMetrics.CacheMisses.Add(1)

	recorder := &ResponseRecorder{ResponseWriter: c.Writer, statusCode: 200}
	c.Writer = recorder
	proxyDockerAuthOriginalWithDomain(c, targetDomain)

	if recorder.statusCode == 200 && len(recorder.body) > 0 {
		ttl := utils.ExtractTTLFromResponse(recorder.body)
		utils.GlobalCache.SetToken(cacheKey, string(recorder.body), ttl)
	}
	c.Writer = recorder.ResponseWriter
	c.Data(recorder.statusCode, "application/json", recorder.body)
}

// ResponseRecorder 捕获上游响应（用于缓存可重放）
type ResponseRecorder struct {
	gin.ResponseWriter
	statusCode int
	body       []byte
}

func (r *ResponseRecorder) WriteHeader(code int) { r.statusCode = code }
func (r *ResponseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return len(data), nil
}

func proxyDockerAuthOriginalWithDomain(c *gin.Context, targetDomain string) {
	authURL := "https://auth.docker.io" + c.Request.URL.Path
	if targetDomain != "" {
		if mapping, found := getRegistryMapping(targetDomain); found {
			authURL = buildAuthURL(mapping.AuthHost, c.Request.URL.Path)
		}
	}
	if c.Request.URL.RawQuery != "" {
		authURL += "?" + c.Request.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, authURL, c.Request.Body)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to create request")
		return
	}
	for k, vals := range c.Request.Header {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Del("Host")

	resp, err := utils.GetGlobalHTTPClient().Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Auth request failed")
		return
	}
	defer resp.Body.Close()

	proxyHost := c.Request.Host
	if proxyHost == "" {
		proxyHost = fmt.Sprintf("localhost:%d", config.GetConfig().Server.Port)
	}
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
	}

	for k, vals := range resp.Header {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range vals {
			if k == "Www-Authenticate" {
				v = rewriteAuthHeader(v, proxyHost, scheme)
			}
			c.Header(k, v)
		}
	}
	c.Status(resp.StatusCode)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		utils.Logger().Warn("copy auth response failed", "err", err)
	}
}

func buildAuthURL(authHost, requestPath string) string {
	authHost = strings.TrimSpace(authHost)
	if authHost == "" {
		return "https://auth.docker.io" + requestPath
	}
	base := "https://" + strings.TrimRight(authHost, "/")
	if strings.Contains(strings.TrimPrefix(authHost, "https://"), "/") || strings.Contains(strings.TrimPrefix(authHost, "http://"), "/") {
		if strings.HasPrefix(authHost, "http://") || strings.HasPrefix(authHost, "https://") {
			return strings.TrimRight(authHost, "/")
		}
		return base
	}
	return base + requestPath
}

func detectAuthTargetDomain(c *gin.Context) string {
	service := c.Query("service")
	if _, ok := getRegistryMapping(service); ok {
		return service
	}
	if strings.Contains(service, "ghcr.io") {
		return "ghcr.io"
	}
	if strings.Contains(service, "gcr.io") {
		return "gcr.io"
	}
	if strings.Contains(service, "quay.io") {
		return "quay.io"
	}
	if strings.Contains(service, "registry.k8s.io") {
		return "registry.k8s.io"
	}
	return ""
}

// rewriteAuthHeader 重写认证头，将上游认证 URL 替换成代理地址
func rewriteAuthHeader(authHeader, proxyHost, scheme string) string {
	for _, host := range []string{"https://auth.docker.io", "https://ghcr.io", "https://gcr.io", "https://quay.io"} {
		authHeader = strings.ReplaceAll(authHeader, host, scheme+"://"+proxyHost)
	}
	return authHeader
}
