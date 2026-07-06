#!/bin/sh
set -e

warn() {
    echo "hubproxy: $1"
}

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
