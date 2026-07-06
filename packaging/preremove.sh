#!/bin/sh
set -e

case "${1:-}" in
    1|upgrade)
        exit 0
        ;;
esac

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop hubproxy >/dev/null 2>&1 || true
    systemctl disable hubproxy >/dev/null 2>&1 || true
fi

if command -v rc-service >/dev/null 2>&1; then
    rc-service hubproxy stop >/dev/null 2>&1 || true
fi

if command -v rc-update >/dev/null 2>&1; then
    rc-update del hubproxy default >/dev/null 2>&1 || true
fi
