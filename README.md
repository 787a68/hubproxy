# HubProxy

<p align="center">
  <strong>Docker & GitHub 加速代理服务器 · 极速 · 轻量 · 自托管</strong>
</p>

<p align="center">
  <a href="https://github.com/787a68/hubproxy/actions"><img src="https://github.com/787a68/hubproxy/actions/workflows/build-docker.yml/badge.svg" alt="Build"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/787a68/hubproxy" alt="Go version">
  <img src="https://img.shields.io/github/license/787a68/hubproxy" alt="License">
  <img src="https://img.shields.io/github/v/release/787a68/hubproxy" alt="Release">
</p>

---

## ✨ 特性

- 🐳 **Docker 镜像加速** — 支持 Docker Hub、GHCR、GCR、Quay、k8s.io 等多个上游
- 🐳 **离线镜像包** — 流式打包下载，单镜像或批量打包，支持多平台选择
- 📁 **GitHub 文件加速** — Release / Raw / API / Archive，智能重定向跟随
- 🤖 **AI 模型仓库** — 支持 Hugging Face 模型下载加速
- ⚡ **极致性能** —
  - `atomic.Pointer` 无锁配置读取
  - 64 分片缓存 + LRU，零争用
  - 64 分片限流器，独立锁，低争用
  - FNV-1a 哈希替代 MD5 分片选择
  - 慢请求 + 错误日志（默认不记每请求）
  - 结构化 `slog` 日志（JSON 格式）
- 🛡️ **智能限流** — IPv4 / IPv6，IPv6 /64 段聚合，CIDR 白/黑名单
- 🚫 **仓库审计** — 支持镜像名与 GitHub 仓库的通配符黑白名单
- 🔍 **镜像搜索** — 在线搜索 Docker Hub，带缓存与重试
- 📊 **可观测性** —
  - `/metrics` Prometheus 指标
  - `/healthz` 健康检查
  - 结构化 JSON 日志 + Request ID
- 🔧 **统一配置** — TOML 文件 + 环境变量覆盖
- 🚀 **单二进制** — Go 静态编译，UPX 压缩，镜像 < 20MB
- 🛡️ **安全** — 容器非 root，HEALTHCHECK，无 OS 依赖

## 📦 快速开始

### Docker（推荐）

```bash
docker run -d \
  --name hubproxy \
  -p 5000:5000 \
  --restart always \
  ghcr.io/787a68/hubproxy:latest
```

### Docker Compose

```yaml
services:
  hubproxy:
    image: ghcr.io/787a68/hubproxy:latest
    container_name: hubproxy
    restart: always
    ports:
      - "5000:5000"
    volumes:
      - ./config.toml:/app/config.toml:ro
```

### 脚本安装（Systemd / OpenRC）

```bash
curl -fsSL https://raw.githubusercontent.com/787a68/hubproxy/main/install.sh | sh
```

## 🚀 使用

### Docker 镜像加速

```bash
# 直接拉取
docker pull yourdomain.com/nginx
docker pull yourdomain.com/ghcr.io/library/nginx

# 或配置为全局镜像加速
# /etc/docker/daemon.json
{
  "registry-mirrors": ["https://yourdomain.com"]
}
```

### GitHub 文件加速

```bash
# 原链接
https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz

# 加速链接
https://yourdomain.com/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz

# git clone 加速
git clone https://yourdomain.com/https://github.com/user/repo.git
```

### 离线镜像包

```bash
# 准备下载令牌
curl "https://yourdomain.com/api/image/download/nginx?mode=prepare"

# 用返回的 URL 下载 tar 包
curl -OJ "https://yourdomain.com/api/image/download/nginx?token=<TOKEN>"

# 批量打包（POST JSON）
curl -X POST -H "Content-Type: application/json" \
  -d '{"images":["nginx:1.25","redis:7"],"useCompressedLayers":true}' \
  "https://yourdomain.com/api/image/batch?mode=prepare"
```

### 在线搜索

```bash
curl "https://yourdomain.com/search?q=nginx"
curl "https://yourdomain.com/tags/library/nginx?page=1&page_size=100"
```

## ⚙️ 配置

完整配置见 [`config.toml`](src/config.toml)。所有字段均可用环境变量覆盖：

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `CONFIG_PATH` | `config.toml` | 配置文件路径 |
| `SERVER_HOST` | `0.0.0.0` | 监听地址 |
| `SERVER_PORT` | `5000` | 监听端口 |
| `ENABLE_H2C` | `false` | 启用 HTTP/2 cleartext |
| `ENABLE_FRONTEND` | `true` | 启用前端页面 |
| `MAX_FILE_SIZE` | `2147483648` | GitHub 文件大小上限（字节） |
| `RATE_LIMIT` | `500` | 每周期请求数 |
| `RATE_PERIOD_HOURS` | `3` | 限流周期（小时） |
| `IP_WHITELIST` | — | IP 白名单（逗号分隔，支持 CIDR） |
| `IP_BLACKLIST` | — | IP 黑名单（逗号分隔，支持 CIDR） |
| `MAX_IMAGES` | `10` | 批量下载镜像数上限 |
| `ACCESS_PROXY` | — | 上游代理（如 `socks5://127.0.0.1:1080`） |

## 📊 监控

```bash
# Prometheus 指标
curl https://yourdomain.com/metrics

# 健康检查
curl https://yourdomain.com/healthz
```

指标包含：`hubproxy_requests_total`、`hubproxy_cache_hits_total`、`hubproxy_cache_misses_total`、`hubproxy_bytes_proxied_total`、`hubproxy_docker_manifest_requests_total`、`hubproxy_docker_blob_requests_total`、`hubproxy_github_requests_total`、`hubproxy_search_requests_total`。

Prometheus scrape 示例：

```yaml
scrape_configs:
  - job_name: 'hubproxy'
    static_configs:
      - targets: ['yourdomain.com:5000']
    metrics_path: /metrics
```

## 🔧 反向代理示例

### Caddy（自动 HTTPS）

```caddy
example.com {
    reverse_proxy 127.0.0.1:5000 {
        header_up X-Real-IP {remote}
        header_up X-Forwarded-For {remote}
        header_up X-Forwarded-Proto {scheme}
    }
}
```

### Cloudflare CDN

```caddy
example.com {
    reverse_proxy 127.0.0.1:5000 {
        header_up X-Forwarded-For {http.request.header.CF-Connecting-IP}
        header_up X-Real-IP {http.request.header.CF-Connecting-IP}
        header_up X-Forwarded-Proto https
    }
}
```

## 🏗️ 部署架构

```
Docker客户端 ──→ HubProxy ──→ go-containerregistry ──→ 上游Registry
                     │              ↕ (内部处理401+token)
                     │              ──→ 认证服务器
                     ↓
              /v2/*  /token/*
              /api/image/*  (离线包)
              /search /tags  (搜索)
              /https://github.com/*  (GitHub代理)
              /metrics  /healthz  (监控)
```

## 🛠️ 开发

```bash
# 本地运行
cd src && go run .

# 测试
go test ./... -race

# 构建
CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=v1.0.0" -trimpath -o hubproxy .

# Docker 构建多架构
docker buildx build --platform linux/amd64,linux/arm64 -t hubproxy:test .
```

## 📋 服务管理

```bash
# systemd
sudo systemctl status|restart|stop hubproxy
sudo journalctl -u hubproxy -f

# OpenRC (Alpine)
sudo rc-service hubproxy status|restart|stop
sudo tail -f /var/log/hubproxy.log

# 编辑配置
sudo vi /etc/hubproxy/config.toml
```

## 📁 文件路径

| 路径 | 说明 |
|------|------|
| `/usr/bin/hubproxy` | 二进制文件 |
| `/etc/hubproxy/config.toml` | 配置文件 |
| `/lib/systemd/system/hubproxy.service` | systemd 服务 |
| `/etc/init.d/hubproxy` | OpenRC 服务 |
| `/var/log/hubproxy.log` | Alpine 日志 |

## ⚠️ 免责声明

本程序仅供学习交流使用，请遵守当地法律法规，作者不对使用者的任何行为承担责任。

## 🙏 致谢

本项目 Fork 自 [sky22333/hubproxy](https://github.com/sky22333/hubproxy)，感谢原作者的开源贡献。在上游基础上进行了性能重构与工程化改进。

## 📄 License

MIT