# HubProxy

Docker Registry、GitHub 文件、Hugging Face 文件加速代理。单二进制部署，内置前端页面、限流、缓存、健康检查和 Prometheus 指标。

[![Docker](https://github.com/787a68/hubproxy/actions/workflows/docker-build.yml/badge.svg)](https://github.com/787a68/hubproxy/actions/workflows/docker-build.yml)
[![Release](https://github.com/787a68/hubproxy/actions/workflows/release.yml/badge.svg)](https://github.com/787a68/hubproxy/actions/workflows/release.yml)

## 功能

- Docker 镜像代理：Docker Hub、GHCR、GCR、Quay、registry.k8s.io。
- Docker 离线包：单镜像或批量镜像流式导出 tar，支持平台选择。
- GitHub 文件代理：release、raw、archive、API、git clone、脚本链接改写。
- Hugging Face 文件代理。
- Docker Hub 镜像搜索和标签查询。
- IP 限流、IP 黑白名单、仓库黑白名单。
- `/healthz` 健康检查和 `/metrics` Prometheus 指标。
- 自动跟随系统深色模式的内置前端。

## Docker 镜像标签

自动构建会推送以下标签：

| 标签 | 说明 |
| --- | --- |
| `latest` | 最新主分支多架构镜像 |
| `YYYY.MM.DD` | UTC 日期版本，多架构镜像 |
| `linux-amd64` | 最新 amd64 单架构镜像 |
| `linux-arm64` | 最新 arm64 单架构镜像 |

不会再发布 `main`、`sha-*` 标签，也不会发布 provenance/SBOM 造成的额外无标签 digest 记录。

## 快速启动

```bash
docker run -d \
  --name hubproxy \
  -p 5000:5000 \
  --restart unless-stopped \
  ghcr.io/787a68/hubproxy:latest
```

Docker Compose：

```bash
docker compose up -d
```

`docker-compose.yml` 默认挂载 `./src/config.toml` 到容器内 `/app/config.toml`。

## 二进制安装

每次推送 `v*` 标签或手动触发都会自动构建 Release，产物包括：

- `hubproxy-linux-amd64.tar.gz`
- `hubproxy-linux-arm64.tar.gz`
- `hubproxy-linux-amd64.deb`
- `hubproxy-linux-arm64.deb`
- `hubproxy-linux-amd64.rpm`
- `hubproxy-linux-arm64.rpm`
- `hubproxy-linux-amd64.apk`
- `hubproxy-linux-arm64.apk`

脚本安装：

```bash
curl -fsSL https://raw.githubusercontent.com/787a68/hubproxy/main/install.sh | sh
```

指定版本：

```bash
VERSION=2026.07.06 curl -fsSL https://raw.githubusercontent.com/787a68/hubproxy/main/install.sh | sh
```

## 使用

Docker 镜像代理：

```bash
docker pull your-domain.com/nginx
docker pull your-domain.com/library/nginx
docker pull your-domain.com/ghcr.io/owner/image:tag
docker pull your-domain.com/quay.io/org/image:tag
docker pull your-domain.com/registry.k8s.io/pause:3.9
```

Docker daemon mirror：

```json
{
  "registry-mirrors": ["https://your-domain.com"]
}
```

GitHub 文件代理：

```bash
https://your-domain.com/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz
https://your-domain.com/https://raw.githubusercontent.com/user/repo/main/file.txt
git clone https://your-domain.com/https://github.com/user/repo.git
```

离线镜像包：

```bash
curl "https://your-domain.com/api/image/download/nginx?mode=prepare"
curl -OJ "https://your-domain.com/api/image/download/nginx?token=TOKEN"
```

批量离线包：

```bash
curl -X POST "https://your-domain.com/api/image/batch?mode=prepare" \
  -H "Content-Type: application/json" \
  -d '{"images":["nginx:1.25","redis:7"],"platform":"linux/amd64","useCompressedLayers":true}'
```

搜索：

```bash
curl "https://your-domain.com/search?q=nginx"
curl "https://your-domain.com/tags/library/nginx?page=1&page_size=100"
```

## 配置

默认配置在 `src/config.toml`。启动时默认读取当前工作目录的 `config.toml`，也可以用 `CONFIG_PATH` 指定。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CONFIG_PATH` | `config.toml` | 配置文件路径 |
| `SERVER_HOST` | `0.0.0.0` | 监听地址 |
| `SERVER_PORT` | `5000` | 监听端口 |
| `ENABLE_H2C` | `false` | 启用 h2c |
| `ENABLE_FRONTEND` | `true` | 启用内置前端 |
| `MAX_FILE_SIZE` | `2147483648` | GitHub 文件大小限制，字节 |
| `RATE_LIMIT` | `500` | 单 IP 周期请求数 |
| `RATE_PERIOD_HOURS` | `3` | 限流周期，小时 |
| `IP_WHITELIST` | 空 | IP 白名单，逗号分隔，支持 CIDR |
| `IP_BLACKLIST` | 空 | IP 黑名单，逗号分隔，支持 CIDR |
| `MAX_IMAGES` | `10` | 批量离线包最大镜像数 |
| `ACCESS_PROXY` | 空 | 上游代理，如 `socks5://127.0.0.1:1080` |

## 监控

```bash
curl http://127.0.0.1:5000/healthz
curl http://127.0.0.1:5000/metrics
```

## 本地开发

```bash
cd src
go test ./...
go run .
```

构建：

```bash
cd src
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X 'main.buildVersion=2026.07.06'" -o hubproxy .
```

Docker 构建：

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=2026.07.06 \
  -t ghcr.io/787a68/hubproxy:latest .
```

## 反向代理

Caddy 示例：

```caddy
example.com {
    reverse_proxy 127.0.0.1:5000 {
        header_up X-Real-IP {remote}
        header_up X-Forwarded-For {remote}
        header_up X-Forwarded-Proto {scheme}
    }
}
```

## License

MIT
