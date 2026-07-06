# HubProxy

Docker 镜像、GitHub 文件、Hugging Face 文件加速代理。单二进制，自带前端，自动跟随系统深色模式。

## 功能

- **Docker 镜像代理** — Docker Hub、GHCR、GCR、Quay、registry.k8s.io
- **离线镜像包** — 单镜像或批量流式导出 tar，支持平台选择
- **GitHub 文件代理** — release、raw、archive、API、git clone
- **Hugging Face 代理** — 模型文件下载加速
- **镜像搜索** — 在线搜索 Docker Hub，查看标签和架构
- **限流** — IPv4/IPv6 聚合，按 IP 分片限流，支持按 IP/CIDR 独立限速或阻断
- **仓库审计** — 镜像名和 GitHub 仓库的通配符黑白名单
- **监控** — `/healthz` 健康检查，`/metrics` Prometheus 指标

## Docker 镜像

| 标签 | 说明 |
| --- | --- |
| `latest` | 最新版本，多架构 |
| `YYYY.MM.DD` | 日期版本，多架构 |
| `amd64` | 最新 amd64 |
| `arm64` | 最新 arm64 |

```bash
docker run -d --name hubproxy -p 5000:5000 --restart unless-stopped ghcr.io/787a68/hubproxy:latest
```

Docker Compose：

```bash
docker compose up -d
```

## 二进制安装

每次推送 `main` 或打 `v*` 标签会自动构建 Release，包含 tar.gz、deb、rpm、apk。

```bash
curl -fsSL https://raw.githubusercontent.com/787a68/hubproxy/main/install.sh | sh
```

## 使用

### Docker 镜像加速

```bash
docker pull your-domain.com/nginx
docker pull your-domain.com/ghcr.io/owner/image:tag
docker pull your-domain.com/quay.io/org/image:tag
docker pull your-domain.com/registry.k8s.io/pause:3.9
```

配置为全局镜像加速（`/etc/docker/daemon.json`）：

```json
{
  "registry-mirrors": ["https://your-domain.com"]
}
```

### GitHub 文件加速

```bash
https://your-domain.com/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz
git clone https://your-domain.com/https://github.com/user/repo.git
```

### 离线镜像包

```bash
# 单镜像
curl "https://your-domain.com/api/image/download/nginx?mode=prepare"
curl -OJ "https://your-domain.com/api/image/download/nginx?token=TOKEN"

# 批量
curl -X POST "https://your-domain.com/api/image/batch?mode=prepare" \
  -H "Content-Type: application/json" \
  -d '{"images":["nginx:1.25","redis:7"],"platform":"linux/amd64"}'
```

### 镜像搜索

```bash
curl "https://your-domain.com/search?q=nginx"
curl "https://your-domain.com/tags/library/nginx?page=1&page_size=100"
```

### 监控

```bash
curl http://127.0.0.1:5000/healthz
curl http://127.0.0.1:5000/metrics
```

## 配置

默认配置见 `src/config.toml`，所有字段可用环境变量覆盖：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CONFIG_PATH` | `config.toml` | 配置文件路径 |
| `SERVER_HOST` | `0.0.0.0` | 监听地址 |
| `SERVER_PORT` | `5000` | 监听端口 |
| `ENABLE_H2C` | `false` | 启用 h2c |
| `ENABLE_FRONTEND` | `true` | 启用前端 |
| `MAX_FILE_SIZE` | `2147483648` | GitHub 文件大小限制（字节） |
| `RATE_LIMIT` | `500` | 单 IP 周期请求数 |
| `RATE_PERIOD_HOURS` | `3` | 限流周期（小时） |
| `MAX_IMAGES` | `10` | 批量离线包最大镜像数 |
| `ACCESS_PROXY` | — | 上游代理，如 `socks5://127.0.0.1:1080` |

按 IP/CIDR 独立限速在 `config.toml` 的 `[ipLimits]` 段配置，值为每周期请求数，`0` 表示阻断：

```toml
[ipLimits]
"192.168.1.100" = 1000
"10.0.0.0/8" = 200
"172.16.0.1" = 0
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

## 致谢

本项目基于 [sky22333/hubproxy](https://github.com/sky22333/hubproxy) 改进，感谢原作者的开源贡献。

## License

MIT
