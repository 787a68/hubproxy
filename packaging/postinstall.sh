#!/bin/sh
set -e

warn() {
    echo "hubproxy: $1"
}

# 创建系统用户（幂等）
if ! getent passwd hubproxy >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
        useradd --system --no-create-home --shell /usr/sbin/nologin hubproxy || warn "创建用户 hubproxy 失败"
    elif command -v adduser >/dev/null 2>&1; then
        adduser --system --no-create-home --disabled-password hubproxy || warn "创建用户 hubproxy 失败"
    elif command -v addgroup >/dev/null 2>&1 && command -v adduser >/dev/null 2>&1; then
        addgroup -S hubproxy 2>/dev/null || true
        adduser -S -D -H -s /sbin/nologin -G hubproxy hubproxy || warn "创建用户 hubproxy 失败"
    else
        warn "无法创建系统用户 hubproxy，请手动创建"
    fi
fi

# 确保配置目录存在且日志文件可写
mkdir -p /etc/hubproxy 2>/dev/null || true
chown -R hubproxy:hubproxy /etc/hubproxy 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || warn "systemd reload failed"
    systemctl enable hubproxy >/dev/null 2>&1 || warn "systemd enable failed"

    if [ -d /run/systemd/system ]; then
        systemctl restart hubproxy || systemctl start hubproxy || {
            warn "service start failed, check: journalctl -u hubproxy"
        }
    fi
fi

if command -v rc-update >/dev/null 2>&1; then
    rc-update add hubproxy default >/dev/null 2>&1 || warn "OpenRC enable failed"
fi

if command -v rc-service >/dev/null 2>&1; then
    rc-service hubproxy restart || rc-service hubproxy start || {
        warn "service start failed, check: rc-service hubproxy status"
    }
fi
