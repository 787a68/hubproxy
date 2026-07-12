# 多阶段构建 - 构建阶段
FROM golang:alpine AS builder

ARG TARGETARCH
ARG TARGETOS=linux
# VERSION 留空时由程序自动取当天日期
ARG VERSION=

WORKDIR /app

# 复制源码
COPY src/ ./

# 自动生成 go.mod/go.sum 并下载依赖
RUN go mod init hubproxy && go mod tidy

# 静态编译 + UPX 压缩（-s -w 去 symbol，-trimpath 去路径）
RUN apk add --no-cache upx && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X 'main.buildVersion=${VERSION}'" -trimpath -o hubproxy . && \
    upx -9 hubproxy

# 运行阶段 - 极小化，非 root
FROM alpine:3.22

LABEL org.opencontainers.image.title="HubProxy" \
      org.opencontainers.image.description="Docker & GitHub 加速代理服务器" \
      org.opencontainers.image.source="https://github.com/787a68/hubproxy" \
      org.opencontainers.image.licenses="MIT"

RUN adduser -D -u 1000 hubproxy && \
    mkdir -p /etc/hubproxy && \
    chown hubproxy:hubproxy /etc/hubproxy

WORKDIR /app

COPY --from=builder /app/hubproxy .
COPY --from=builder /app/config.toml /etc/hubproxy/config.toml

USER hubproxy

EXPOSE 5000

ENV CONFIG_PATH=/etc/hubproxy/config.toml

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:5000/healthz || exit 1

CMD ["./hubproxy"]