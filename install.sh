#!/bin/sh
set -eu

REPO="${REPO:-787a68/hubproxy}"
VERSION="${VERSION:-latest}"
TMP_DIR="${TMP_DIR:-/tmp/hubproxy-install}"

log() {
    printf '%s\n' "$*"
}

fail() {
    printf 'HubProxy 安装失败：%s\n' "$*" >&2
    exit 1
}

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || fail "缺少必要命令：$1"
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            fail "不支持的系统架构：$(uname -m)"
            ;;
    esac
}

detect_packager() {
    if command -v apk >/dev/null 2>&1; then
        echo "apk"
    elif command -v apt-get >/dev/null 2>&1; then
        echo "deb"
    elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1 || command -v rpm >/dev/null 2>&1; then
        echo "rpm"
    else
        fail "不支持的系统：需要 apt、dnf、yum、rpm 或 apk"
    fi
}

asset_name() {
    packager="$1"
    arch="$2"

    case "$packager:$arch" in
        deb:amd64|rpm:amd64|apk:amd64) echo "hubproxy-linux-amd64.${packager}" ;;
        deb:arm64|rpm:arm64|apk:arm64) echo "hubproxy-linux-arm64.${packager}" ;;
        *) fail "不支持的安装包目标：${packager}/${arch}" ;;
    esac
}

asset_url() {
    asset="$1"

    if [ "$VERSION" = "latest" ]; then
        echo "https://github.com/${REPO}/releases/latest/download/${asset}"
    else
        echo "https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
    fi
}

install_package() {
    package_file="$1"
    packager="$2"

    case "$packager" in
        deb)
            apt-get install -y "$package_file"
            ;;
        rpm)
            if command -v dnf >/dev/null 2>&1; then
                dnf install -y "$package_file"
            elif command -v yum >/dev/null 2>&1; then
                yum install -y "$package_file"
            else
                rpm -Uvh "$package_file"
            fi
            ;;
        apk)
            apk add --allow-untrusted "$package_file"
            ;;
        *)
            fail "不支持的包管理器：$packager"
            ;;
    esac
}

if [ "$(id -u)" -ne 0 ]; then
    fail "请使用 root 权限运行"
fi

need_cmd curl

ARCH="$(detect_arch)"
PACKAGER="$(detect_packager)"

rm -rf "$TMP_DIR"
mkdir -p "$TMP_DIR"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

log "安装 HubProxy：linux/${ARCH}（${PACKAGER}）"

ASSET="$(asset_name "$PACKAGER" "$ARCH")"
ASSET_URL="$(asset_url "$ASSET")"

PACKAGE_FILE="${TMP_DIR}/$(basename "$ASSET_URL")"
log "下载安装包..."
curl -fL --retry 3 --retry-delay 2 -o "$PACKAGE_FILE" "$ASSET_URL" || fail "下载安装包失败"

log "安装软件包..."
install_package "$PACKAGE_FILE" "$PACKAGER"

log "安装完成"
log "默认端口：5000"
log "配置文件：/etc/hubproxy/config.toml"
