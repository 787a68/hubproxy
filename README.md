# HubProxy

Docker 镜像、GitHub 文件、Hugging Face 文件下载加速。单二进制，docker。

## 功能

- **Docker 镜像代理** — Docker Hub、GHCR、GCR、Quay、registry.k8s.io
- **离线镜像包** — 单镜像或批量流式导出 tar，支持平台选择
- **GitHub 文件代理** — release、raw、archive、API、git clone
- **Hugging Face 代理** — 模型文件下载加速
- **镜像搜索** — 在线搜索 Docker Hub，查看标签和架构
- **限流** — 按 IP 分片限速，支持按 IP/CIDR 独立限速或阻断
- **仓库审计** — 镜像名和 GitHub 仓库的通配符黑白名单
- **监控** — `/healthz` 健康检查，`/metrics` Prometheus 指标

## 安装

### Docker

每次推送 `main` 自动构建，标签如下：

| 标签 | 说明 |
| --- | --- |
| `latest` | 最新版本，自动识别架构 |
| `arm64` | 最新 ARM64 版本 |
| `YYYY.MM.DD` | 指定版本，自动识别架构 |

```bash
docker run -d --name hubproxy -p 5000:5000 --restart unless-stopped ghcr.io/787a68/hubproxy:latest
```

### Docker Compose

```bash
docker compose up -d
```

### 二进制

每次推送 `main` 自动构建 Release，包含 tar.gz、deb、rpm、apk。

```bash
curl -fsSL https://raw.githubusercontent.com/787a68/hubproxy/main/install.sh | sh
```

指定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/787a68/hubproxy/main/install.sh | VERSION=2026.07.07 sh
```

## 使用

### Docker 镜像加速

```bash
docker pull your-domain.com/nginx
docker pull your-domain.com/ghcr.io/owner/image:tag
docker pull your-domain.com/quay.io/org/image:tag
docker pull your-domain.com/registry.k8s.io/pause:3.9
```

全局镜像加速（`/etc/docker/daemon.json`）：

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

默认配置见 `src/config.toml`。所有字段可用环境变量覆盖，环境变量优先于配置文件。

| 配置文件 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `[server] addr` | `ADDR` | `0.0.0.0:5000` | 监听地址 |
| `[server] fileSize` | `FILE_SIZE` | `1073741824` | GitHub 文件大小限制（字节），1GB |
| `[server] enableH2C` | `H2C` | `true` | 启用 HTTP/2 cleartext |
| `[server] enableFrontend` | `FRONTEND` | `true` | 启用前端页面 |
| `ipLimits` | `IP_LIMITS` | `* 3 500` | 限速规则，格式 `IP/CIDR 周期(小时) 请求数`，逗号分隔，`0` 阻断 |
| `[access] whiteList` | `WHITELIST` | 空 | 仓库白名单，逗号分隔，支持通配符 |
| `[access] blackList` | `BLACKLIST` | 空 | 仓库黑名单，逗号分隔，支持通配符 |
| `[access] proxy` | `PROXY` | 空 | 上游代理，如 `socks5://127.0.0.1:1080` |
| `[download] maxImages` | `MAX_IMAGES` | `10` | 批量离线包最大镜像数 |
| `cache` | `CACHE` | `20m` | 缓存 TTL，`off` 禁用，否则 Go duration 格式 |
| `logLevel` | `LOG_LEVEL` | `info` | 日志等级：`debug`/`info`/`warn`/`error` |
| `logFile` | `LOG_FILE` | 自动 | 日志文件路径，为空时自动写入 `CONFIG_PATH` 所在目录下的 `hubproxy.log` |
| — | `CONFIG_PATH` | `config.toml` | 配置文件路径 |

### 限速规则

格式 `IP/CIDR 周期(小时) 请求数`，逗号分隔。`"*"` 是全局限速，其他 IP/CIDR 是覆盖规则，请求数 `0` 表示阻断。

配置文件：

```toml
ipLimits = [
    "* 3 500",
    "10.0.0.0/8 1 200",
    "172.16.0.1 0 0",
]
```

环境变量：

```bash
IP_LIMITS="* 3 500,10.0.0.0/8 1 200,172.16.0.1 0 0"
```

### 仓库审计通配符

`baduser/*`、`*/malicious-repo`、`baduser/malicious-repo`

### Registry 映射

仅支持配置文件，不支持环境变量。默认已配置 GHCR、GCR、Quay、registry.k8s.io。

```toml
[registries."ghcr.io"]
upstream = "ghcr.io"
authHost = "ghcr.io/token"
authType = "github"
enabled = true
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
