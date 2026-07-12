package handlers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"hubproxy/config"
	"hubproxy/utils"
)

// DebounceEntry 防抖条目
type DebounceEntry struct {
	LastRequest time.Time
}

// DownloadDebouncer 下载防抖器
type DownloadDebouncer struct {
	mu          sync.Mutex
	entries     map[string]*DebounceEntry
	window      time.Duration
	lastCleanup time.Time
}

// NewDownloadDebouncer 创建下载防抖器
func NewDownloadDebouncer(window time.Duration) *DownloadDebouncer {
	return &DownloadDebouncer{
		entries:     make(map[string]*DebounceEntry),
		window:      window,
		lastCleanup: time.Now(),
	}
}

// ShouldAllow 检查是否应该允许请求
func (d *DownloadDebouncer) ShouldAllow(userID, contentKey string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := userID + ":" + contentKey
	now := time.Now()

	if entry, exists := d.entries[key]; exists {
		if now.Sub(entry.LastRequest) < d.window {
			return false
		}
	}

	d.entries[key] = &DebounceEntry{
		LastRequest: now,
	}

	if time.Since(d.lastCleanup) > 5*time.Minute {
		d.cleanup(now)
		d.lastCleanup = now
	}

	return true
}

// cleanup 清理过期条目
func (d *DownloadDebouncer) cleanup(now time.Time) {
	for key, entry := range d.entries {
		if now.Sub(entry.LastRequest) > d.window*2 {
			delete(d.entries, key)
		}
	}
}

// generateContentFingerprint 生成内容指纹
func generateContentFingerprint(images []string, platform string) string {
	sortedImages := make([]string, len(images))
	copy(sortedImages, images)
	sort.Strings(sortedImages)

	content := strings.Join(sortedImages, "|") + ":" + platform

	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

// getUserID 获取用户标识（基于 IP + UserAgent 的哈希）
func getUserID(c *gin.Context) string {
	ip, userAgent := getClientIdentity(c)
	hash := md5.Sum([]byte(ip + ":" + userAgent))
	return "ip:" + hex.EncodeToString(hash[:8])
}

// getClientIdentity 获取客户端 IP 和 UserAgent
func getClientIdentity(c *gin.Context) (string, string) {
	ip := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")
	if userAgent == "" {
		userAgent = "unknown"
	}
	return ip, userAgent
}

var (
	singleImageDebouncer *DownloadDebouncer
	batchImageDebouncer  *DownloadDebouncer
)

// InitDebouncer 初始化防抖器
func InitDebouncer() {
	singleImageDebouncer = NewDownloadDebouncer(5 * time.Second)
	batchImageDebouncer = NewDownloadDebouncer(60 * time.Second)
}

type BatchDownloadRequest struct {
	Images              []string
	Platform            string
	UseCompressedLayers bool
}

type SingleDownloadRequest struct {
	Image               string
	Platform            string
	UseCompressedLayers bool
}

type tokenEntry[T any] struct {
	Request   T
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

type tokenStore[T any] struct {
	mu      sync.Mutex
	entries map[string]tokenEntry[T]
}

const downloadTokenTTL = 2 * time.Minute
const downloadTokenMaxEntries = 2000

func newTokenStore[T any]() *tokenStore[T] {
	return &tokenStore[T]{
		entries: make(map[string]tokenEntry[T]),
	}
}

func (s *tokenStore[T]) create(req T, ip, userAgent string) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := time.Now()
	entry := tokenEntry[T]{
		Request:   req,
		ExpiresAt: now.Add(downloadTokenTTL),
		IP:        ip,
		UserAgent: userAgent,
	}

	s.mu.Lock()
	s.cleanup(now)
	if len(s.entries) >= downloadTokenMaxEntries {
		s.mu.Unlock()
		return "", fmt.Errorf("令牌过多，请稍后再试")
	}
	s.entries[token] = entry
	s.mu.Unlock()

	return token, nil
}

func (s *tokenStore[T]) consume(token, ip, userAgent string) (T, bool) {
	var empty T
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.entries[token]
	if !exists {
		return empty, false
	}
	if now.After(entry.ExpiresAt) {
		delete(s.entries, token)
		return empty, false
	}
	if entry.IP != ip || entry.UserAgent != userAgent {
		delete(s.entries, token)
		return empty, false
	}
	delete(s.entries, token)
	return entry.Request, true
}

func (s *tokenStore[T]) cleanup(now time.Time) {
	for token, entry := range s.entries {
		if now.After(entry.ExpiresAt) {
			delete(s.entries, token)
		}
	}
}

var batchDownloadTokens = newTokenStore[BatchDownloadRequest]()
var singleDownloadTokens = newTokenStore[SingleDownloadRequest]()

// ImageStreamer 镜像流式下载器
type ImageStreamer struct {
	remoteOptions []remote.Option
}

// NewImageStreamer 创建镜像下载器
func NewImageStreamer() *ImageStreamer {
	remoteOptions := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithTransport(utils.GetGlobalHTTPClient().Transport),
	}

	return &ImageStreamer{
		remoteOptions: remoteOptions,
	}
}

// StreamOptions 下载选项
type StreamOptions struct {
	Platform            string
	Compression         bool
	UseCompressedLayers bool
}

// resolveImage 解析镜像引用并返回对应的 v1.Image
func (is *ImageStreamer) resolveImage(ctx context.Context, imageRef string, options *StreamOptions) (v1.Image, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("解析镜像引用失败: %w", err)
	}

	contextOptions := append(is.remoteOptions, remote.WithContext(ctx))

	desc, err := remote.Get(ref, contextOptions...)
	if err != nil {
		return nil, fmt.Errorf("获取镜像描述失败: %w", err)
	}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		return is.selectPlatformImage(desc, options)
	default:
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("获取镜像失败: %w", err)
		}
		return img, nil
	}
}

func setDownloadHeaders(c *gin.Context, filename string, compressed bool) {
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	if compressed {
		c.Header("Content-Encoding", "gzip")
	}
}

// StreamImageToGin 流式响应到Gin
func (is *ImageStreamer) StreamImageToGin(ctx context.Context, imageRef string, c *gin.Context, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{UseCompressedLayers: true}
	}

	filename := strings.ReplaceAll(imageRef, "/", "_") + ".tar"
	setDownloadHeaders(c, filename, options.Compression)

	utils.Logger().Info("downloading image", "ref", imageRef)

	img, err := is.resolveImage(ctx, imageRef, options)
	if err != nil {
		return err
	}

	return is.streamImageLayers(ctx, img, c.Writer, options, imageRef)
}

// streamImageLayers 处理镜像层
func (is *ImageStreamer) streamImageLayers(ctx context.Context, img v1.Image, writer io.Writer, options *StreamOptions, imageRef string) error {
	var finalWriter io.Writer = writer

	if options.Compression {
		gzWriter := gzip.NewWriter(writer)
		defer gzWriter.Close()
		finalWriter = gzWriter
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	configFile, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("获取镜像配置失败: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("获取镜像层失败: %w", err)
	}

	utils.Logger().Debug("image layers", "count", len(layers))

	manifest, repositories, err := is.writeImageToTar(ctx, tarWriter, img, layers, configFile, imageRef, options)
	if err != nil {
		return err
	}

	return is.writeManifestFiles(tarWriter, []map[string]interface{}{manifest}, repositories)
}

// writeImageToTar 将镜像 config 和 layers 写入 tar，返回 manifest 和 repositories 信息
func (is *ImageStreamer) writeImageToTar(ctx context.Context, tarWriter *tar.Writer, img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile, imageRef string, options *StreamOptions) (map[string]interface{}, map[string]map[string]string, error) {
	configDigest, err := img.ConfigName()
	if err != nil {
		return nil, nil, err
	}

	configData, err := json.Marshal(configFile)
	if err != nil {
		return nil, nil, err
	}

	configName := configDigest.Hex + ".json"
	configHeader := &tar.Header{
		Name: configName,
		Size: int64(len(configData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(configHeader); err != nil {
		return nil, nil, err
	}
	if _, err := tarWriter.Write(configData); err != nil {
		return nil, nil, err
	}

	layerDirs := make([]string, len(layers))
	for i, layer := range layers {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		dir, err := is.writeLayerToTar(tarWriter, layer, i, len(layers), options)
		if err != nil {
			return nil, nil, err
		}
		layerDirs[i] = dir
	}

	manifest := map[string]interface{}{
		"Config":   configName,
		"RepoTags": []string{imageRef},
		"Layers": func() []string {
			var dirs []string
			for _, dir := range layerDirs {
				dirs = append(dirs, dir+"/layer.tar")
			}
			return dirs
		}(),
	}

	repositories := make(map[string]map[string]string)
	parts := strings.Split(imageRef, ":")
	if len(parts) == 2 {
		repoName := parts[0]
		tag := parts[1]
		repositories[repoName] = map[string]string{tag: configDigest.Hex}
	}

	return manifest, repositories, nil
}

// writeLayerToTar 写入单个 layer 到 tar，返回 layer 目录名
func (is *ImageStreamer) writeLayerToTar(tarWriter *tar.Writer, layer v1.Layer, index, total int, options *StreamOptions) (string, error) {
	digest, err := layer.Digest()
	if err != nil {
		return "", err
	}
	layerDir := digest.Hex

	layerHeader := &tar.Header{
		Name:     layerDir + "/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	}
	if err := tarWriter.WriteHeader(layerHeader); err != nil {
		return "", err
	}

	var layerSize int64
	var layerReader io.ReadCloser

	if options != nil && options.UseCompressedLayers {
		layerSize, err = layer.Size()
		if err != nil {
			return "", err
		}
		layerReader, err = layer.Compressed()
	} else {
		layerSize, err = partial.UncompressedSize(layer)
		if err != nil {
			return "", err
		}
		layerReader, err = layer.Uncompressed()
	}
	if err != nil {
		return "", err
	}
	defer layerReader.Close()

	layerTarHeader := &tar.Header{
		Name: layerDir + "/layer.tar",
		Size: layerSize,
		Mode: 0644,
	}
	if err := tarWriter.WriteHeader(layerTarHeader); err != nil {
		return "", err
	}
	if _, err := io.Copy(tarWriter, layerReader); err != nil {
		return "", err
	}

	utils.Logger().Debug("layer processed", "index", index+1, "total", total)
	return layerDir, nil
}

// writeManifestFiles 写入 manifest.json 和 repositories 文件
func (is *ImageStreamer) writeManifestFiles(tarWriter *tar.Writer, manifests []map[string]interface{}, repositories map[string]map[string]string) error {
	manifestData, err := json.Marshal(manifests)
	if err != nil {
		return err
	}
	manifestHeader := &tar.Header{
		Name: "manifest.json",
		Size: int64(len(manifestData)),
		Mode: 0644,
	}
	if err := tarWriter.WriteHeader(manifestHeader); err != nil {
		return err
	}
	if _, err := tarWriter.Write(manifestData); err != nil {
		return err
	}

	repositoriesData, err := json.Marshal(repositories)
	if err != nil {
		return err
	}
	repositoriesHeader := &tar.Header{
		Name: "repositories",
		Size: int64(len(repositoriesData)),
		Mode: 0644,
	}
	if err := tarWriter.WriteHeader(repositoriesHeader); err != nil {
		return err
	}
	_, err = tarWriter.Write(repositoriesData)
	return err
}

func (is *ImageStreamer) streamSingleImageForBatch(ctx context.Context, tarWriter *tar.Writer, imageRef string, options *StreamOptions) (map[string]interface{}, map[string]map[string]string, error) {
	img, err := is.resolveImage(ctx, imageRef, options)
	if err != nil {
		return nil, nil, err
	}

	layers, err := img.Layers()
	if err != nil {
		return nil, nil, fmt.Errorf("获取镜像层失败: %w", err)
	}

	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, nil, fmt.Errorf("获取镜像配置失败: %w", err)
	}

	utils.Logger().Debug("image layers", "count", len(layers))

	return is.writeImageToTar(ctx, tarWriter, img, layers, configFile, imageRef, options)
}

// selectPlatformImage 从多架构镜像中选择合适的平台镜像
func (is *ImageStreamer) selectPlatformImage(desc *remote.Descriptor, options *StreamOptions) (v1.Image, error) {
	index, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("获取镜像索引失败: %w", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("获取索引清单失败: %w", err)
	}

	// 解析用户指定的平台（支持模糊匹配，如 linux/arm 匹配 linux/arm/v7）
	var userPlatform *v1.Platform
	if options.Platform != "" {
		userPlatform, err = v1.ParsePlatform(options.Platform)
		if err != nil {
			return nil, fmt.Errorf("无效的平台格式 %q: %w", options.Platform, err)
		}
	}

	var selectedDesc *v1.Descriptor
	for _, m := range manifest.Manifests {
		if m.Platform == nil {
			continue
		}

		if userPlatform != nil {
			// 用户指定平台：用 Satisfies 模糊匹配（缺失字段通配）
			if m.Platform.Satisfies(*userPlatform) {
				selectedDesc = &m
				break
			}
		} else if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			// 自动选择：默认 linux/amd64
			selectedDesc = &m
			break
		}
	}

	if selectedDesc == nil && len(manifest.Manifests) > 0 {
		selectedDesc = &manifest.Manifests[0]
		// fallback 时优先选同 OS 的 manifest
		if userPlatform != nil {
			for _, m := range manifest.Manifests {
				if m.Platform != nil && m.Platform.OS == userPlatform.OS {
					selectedDesc = &m
					break
				}
			}
			utils.Logger().Warn("platform not found, falling back",
				"requested", options.Platform, "fallback",
				selectedDesc.Platform.OS+"/"+selectedDesc.Platform.Architecture)
		}
	}

	if selectedDesc == nil {
		return nil, fmt.Errorf("未找到合适的平台镜像")
	}

	img, err := index.Image(selectedDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("获取选中镜像失败: %w", err)
	}

	return img, nil
}

var globalImageStreamer *ImageStreamer

// InitImageStreamer 初始化镜像下载器
func InitImageStreamer() {
	globalImageStreamer = NewImageStreamer()
}

// formatPlatformText 格式化平台文本
func formatPlatformText(platform string) string {
	if platform == "" {
		return "自动选择"
	}
	return platform
}

// InitImageTarRoutes 初始化镜像下载路由
func InitImageTarRoutes(router *gin.Engine) {
	imageAPI := router.Group("/api/image")
	{
		imageAPI.GET("/download/:image", handleDirectImageDownload)
		imageAPI.GET("/info/:image", handleImageInfo)
		imageAPI.GET("/batch", handleBatchDownloadByToken)
		imageAPI.POST("/batch", handleBatchDownloadPrepare)
	}
}

// handleDirectImageDownload 处理单镜像下载
func handleDirectImageDownload(c *gin.Context) {
	imageParam := c.Param("image")
	if imageParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少镜像参数"})
		return
	}

	imageRef := strings.ReplaceAll(imageParam, "_", "/")
	platform := c.Query("platform")
	tag := c.DefaultQuery("tag", "")
	useCompressed := c.DefaultQuery("compressed", "true") == "true"

	if tag != "" && !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":" + tag
	} else if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":latest"
	}

	if _, err := name.ParseReference(imageRef); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像引用格式错误: " + err.Error()})
		return
	}
	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(imageRef); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}

	if c.Query("mode") == "prepare" {
		userID := getUserID(c)
		contentKey := generateContentFingerprint([]string{imageRef}, platform)

		if !singleImageDebouncer.ShouldAllow(userID, contentKey) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "请求过于频繁，请稍后再试",
				"retry_after": 5,
			})
			return
		}

		ip, userAgent := getClientIdentity(c)
		token, err := singleDownloadTokens.create(SingleDownloadRequest{
			Image:               imageRef,
			Platform:            platform,
			UseCompressedLayers: useCompressed,
		}, ip, userAgent)
		if err != nil {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}

		downloadURL := fmt.Sprintf("/api/image/download/%s?token=%s", imageParam, token)
		if tag != "" {
			downloadURL = downloadURL + "&tag=" + url.QueryEscape(tag)
		}
		c.JSON(http.StatusOK, gin.H{"download_url": downloadURL})
		return
	}

	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少下载令牌"})
		return
	}

	ip, userAgent := getClientIdentity(c)
	req, ok := singleDownloadTokens.consume(token, ip, userAgent)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效或过期的下载令牌"})
		return
	}
	if req.Image != imageRef {
		c.JSON(http.StatusBadRequest, gin.H{"error": "下载令牌与镜像不匹配"})
		return
	}
	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(req.Image); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}

	options := &StreamOptions{
		Platform:            req.Platform,
		Compression:         false,
		UseCompressedLayers: req.UseCompressedLayers,
	}

	ctx := c.Request.Context()
	utils.Logger().Info("downloading image", "image", req.Image, "platform", formatPlatformText(req.Platform))

	if err := globalImageStreamer.StreamImageToGin(ctx, req.Image, c, options); err != nil {
		utils.Logger().Error("image download failed", "image", req.Image, "err", err)
		c.String(http.StatusInternalServerError, "镜像下载失败")
		return
	}
}

// handleBatchDownloadByToken 处理批量下载（GET，消费 token 下载）
func handleBatchDownloadByToken(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少下载令牌"})
		return
	}

	ip, userAgent := getClientIdentity(c)
	req, ok := batchDownloadTokens.consume(token, ip, userAgent)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效或过期的下载令牌"})
		return
	}

	if len(req.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像列表不能为空"})
		return
	}

	options := &StreamOptions{
		Platform:            req.Platform,
		Compression:         false,
		UseCompressedLayers: req.UseCompressedLayers,
	}

	ctx := c.Request.Context()
	utils.Logger().Info("batch downloading", "count", len(req.Images), "platform", formatPlatformText(req.Platform))

	filename := fmt.Sprintf("batch_%d_images.tar", len(req.Images))

	setDownloadHeaders(c, filename, options.Compression)

	if err := globalImageStreamer.StreamMultipleImages(ctx, req.Images, c.Writer, options); err != nil {
		utils.Logger().Error("batch download failed", "err", err)
		c.String(http.StatusInternalServerError, "批量镜像下载失败")
		return
	}
}

// handleBatchDownloadPrepare 处理批量下载准备（POST，创建 token）
func handleBatchDownloadPrepare(c *gin.Context) {
	if c.Query("mode") != "prepare" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只支持prepare模式"})
		return
	}

	var req struct {
		Images              []string `json:"images" binding:"required"`
		Platform            string   `json:"platform"`
		UseCompressedLayers *bool    `json:"useCompressedLayers"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	if len(req.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像列表不能为空"})
		return
	}
	for i, imageRef := range req.Images {
		if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
			req.Images[i] = imageRef + ":latest"
		}
	}

	for _, imageRef := range req.Images {
		if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(imageRef); !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": reason})
			return
		}
	}

	cfg := config.GetConfig()
	if len(req.Images) > cfg.Download.MaxImages {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("镜像数量超过限制，最大允许: %d", cfg.Download.MaxImages),
		})
		return
	}

	userID := getUserID(c)
	contentKey := generateContentFingerprint(req.Images, req.Platform)

	if !batchImageDebouncer.ShouldAllow(userID, contentKey) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "批量下载请求过于频繁，请稍后再试",
			"retry_after": 60,
		})
		return
	}

	useCompressed := true
	if req.UseCompressedLayers != nil {
		useCompressed = *req.UseCompressedLayers
	}

	batchReq := BatchDownloadRequest{
		Images:              req.Images,
		Platform:            req.Platform,
		UseCompressedLayers: useCompressed,
	}

	ip, userAgent := getClientIdentity(c)
	token, err := batchDownloadTokens.create(batchReq, ip, userAgent)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"download_url": fmt.Sprintf("/api/image/batch?token=%s", token)})
}

// handleImageInfo 处理镜像信息查询
func handleImageInfo(c *gin.Context) {
	imageParam := c.Param("image")
	if imageParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少镜像参数"})
		return
	}

	imageRef := strings.ReplaceAll(imageParam, "_", "/")
	tag := c.DefaultQuery("tag", "latest")

	if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":" + tag
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像引用格式错误: " + err.Error()})
		return
	}
	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(imageRef); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}

	ctx := c.Request.Context()
	contextOptions := append(globalImageStreamer.remoteOptions, remote.WithContext(ctx))

	desc, err := remote.Get(ref, contextOptions...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取镜像信息失败: " + err.Error()})
		return
	}

	info := gin.H{
		"name":      ref.String(),
		"mediaType": desc.MediaType,
		"digest":    desc.Digest.String(),
		"size":      desc.Size,
	}

	if desc.MediaType == types.OCIImageIndex || desc.MediaType == types.DockerManifestList {
		index, err := desc.ImageIndex()
		if err == nil {
			manifest, err := index.IndexManifest()
			if err == nil {
				var platforms []string
				for _, m := range manifest.Manifests {
					if m.Platform != nil {
						platforms = append(platforms, m.Platform.OS+"/"+m.Platform.Architecture)
					}
				}
				info["platforms"] = platforms
				info["multiArch"] = true
			}
		}
	} else {
		info["multiArch"] = false
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": info})
}

// StreamMultipleImages 批量下载多个镜像
func (is *ImageStreamer) StreamMultipleImages(ctx context.Context, imageRefs []string, writer io.Writer, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{UseCompressedLayers: true}
	}

	var finalWriter io.Writer = writer
	if options.Compression {
		gzWriter := gzip.NewWriter(writer)
		defer gzWriter.Close()
		finalWriter = gzWriter
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	var allManifests []map[string]interface{}
	var allRepositories = make(map[string]map[string]string)

	for i, imageRef := range imageRefs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		utils.Logger().Debug("processing image", "index", i+1, "total", len(imageRefs), "image", imageRef)

		timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		manifest, repositories, err := is.streamSingleImageForBatch(timeoutCtx, tarWriter, imageRef, options)
		cancel()

		if err != nil {
			utils.Logger().Error("download image failed", "image", imageRef, "err", err)
			return fmt.Errorf("下载镜像 %s 失败: %w", imageRef, err)
		}

		if manifest == nil {
			return fmt.Errorf("镜像 %s manifest数据为空", imageRef)
		}

		allManifests = append(allManifests, manifest)

		for repo, tags := range repositories {
			if allRepositories[repo] == nil {
				allRepositories[repo] = make(map[string]string)
			}
			for tag, digest := range tags {
				allRepositories[repo][tag] = digest
			}
		}
	}

	if err := is.writeManifestFiles(tarWriter, allManifests, allRepositories); err != nil {
		return fmt.Errorf("写入manifest文件失败: %w", err)
	}

	utils.Logger().Info("batch download complete", "count", len(imageRefs))
	return nil
}
