#!/bin/sh
# rsagent 一键安装脚本（Linux 与 OpenWrt）：下载对应架构的程序，注册系统服务，可选自动入网。
#
#   curl -fsSL https://raw.githubusercontent.com/mqvista/RemoteSerial/main/deploy/install.sh | sudo sh
#   curl -fsSL .../install.sh | sudo RSAGENT_AUTH_KEY=tskey-client-xxx RSAGENT_TAGS=tag:serial-agent sh
#
# OpenWrt（以 root 登录，没有 curl 时用 wget）：
#   wget -qO- https://raw.githubusercontent.com/mqvista/RemoteSerial/main/deploy/install.sh | sh
#   wget -qO- .../install.sh | sh -s -- --with-tailscale
#   离线：把 rsagent-openwrt-<arch>.tar.gz 解压后执行 sh install.sh
#
# 参数（OpenWrt）：
#   --with-tailscale   系统没有 tailscale 时直接安装，不询问
#   --no-tailscale     不安装系统 tailscale，改用 rsagent 内置的 Tailscale
#
# 环境变量：
#   RSAGENT_VERSION    指定版本（如 0.6.0），默认最新
#   RSAGENT_BASE_URL   下载地址前缀，默认 GitHub Releases；私有仓库或内网改为自建地址
#   RSAGENT_FROM_FILE  跳过下载，直接安装本地程序文件
#   RSAGENT_TAILSCALE  OpenWrt：yes | no | ask（默认 ask），与上面两个参数相同
#   RSAGENT_AUTH_KEY   auth key 或 OAuth client secret，给了就在启动前入网
#   RSAGENT_TAGS       入网 tag，默认 tag:serial-agent
#   RSAGENT_HOSTNAME   节点名（仅内置 Tailscale），默认 rs-<主机名>
#
# 脚本输出为英文，与命令行一致。
set -eu

REPO="mqvista/RemoteSerial"
BIN=/usr/bin/rsagent

say() { printf '%s\n' "$*"; }
die() { say "error: $*" >&2; exit 1; }

TAILSCALE=${RSAGENT_TAILSCALE:-ask}
for a in "$@"; do
    case "$a" in
        --with-tailscale) TAILSCALE=yes ;;
        --no-tailscale) TAILSCALE=no ;;
        *) die "unknown option $a" ;;
    esac
done

[ "$(id -u)" = 0 ] || die "please run as root (sudo)"

OPENWRT=0
[ -f /etc/openwrt_release ] && OPENWRT=1
if [ "$OPENWRT" = 1 ]; then
    DATA=/etc/rsagent
else
    DATA=/var/lib/rsagent
fi

case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture $(uname -m); only amd64 and arm64 are provided" ;;
esac

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fetch() { # url file
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        die "curl or wget is required"
    fi
}

# 解压后的离线包：脚本旁边就有程序
HERE=""
[ -f "$0" ] && HERE=$(cd "$(dirname "$0")" && pwd)
if [ -z "${RSAGENT_FROM_FILE:-}" ] && [ -n "$HERE" ] && [ -f "$HERE/rsagent" ] && [ "$HERE/rsagent" != "$BIN" ]; then
    RSAGENT_FROM_FILE="$HERE/rsagent"
fi

if [ -n "${RSAGENT_FROM_FILE:-}" ]; then
    say "Using local file $RSAGENT_FROM_FILE"
    cp "$RSAGENT_FROM_FILE" "$TMP/rsagent"
else
    if [ -n "${RSAGENT_BASE_URL:-}" ]; then
        BASE=${RSAGENT_BASE_URL%/}
    elif [ -n "${RSAGENT_VERSION:-}" ]; then
        BASE="https://github.com/$REPO/releases/download/v${RSAGENT_VERSION#v}"
    else
        BASE="https://github.com/$REPO/releases/latest/download"
    fi
    say "Downloading $BASE/rsagent-linux-$ARCH"
    fetch "$BASE/rsagent-linux-$ARCH" "$TMP/rsagent"
    if fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS" 2>/dev/null; then
        want=$(grep " rsagent-linux-$ARCH\$" "$TMP/SHA256SUMS" | awk '{print $1}')
        got=$(sha256sum "$TMP/rsagent" | awk '{print $1}')
        [ -n "$want" ] && [ "$want" != "$got" ] && die "SHA-256 mismatch"
        say "SHA-256 verified"
    fi
fi
chmod 755 "$TMP/rsagent"
"$TMP/rsagent" version >/dev/null || die "the downloaded program does not run on this system"

# ---------------------------------------------------------------- OpenWrt
if [ "$OPENWRT" = 1 ]; then
    [ -x /etc/init.d/rsagent ] && /etc/init.d/rsagent stop >/dev/null 2>&1 || true
    cp "$TMP/rsagent" "$BIN.new" && chmod 755 "$BIN.new" && mv "$BIN.new" "$BIN"
    mkdir -p "$DATA"
    chmod 700 "$DATA"
    # 升级固件时保留配置与节点状态
    mkdir -p /lib/upgrade/keep.d
    echo "$DATA/" > /lib/upgrade/keep.d/rsagent
    say "Installed $("$BIN" version)"

    if [ ! -x /usr/sbin/tailscaled ] && [ "$TAILSCALE" = ask ]; then
        if [ -r /dev/tty ]; then
            printf 'The system Tailscale (tailscale package) is not installed. Install it now? [Y/n] ' > /dev/tty
            read -r ans < /dev/tty || ans=""
            case "$ans" in n|N|no|NO) TAILSCALE=no ;; *) TAILSCALE=yes ;; esac
        else
            TAILSCALE=yes
        fi
    fi
    if [ -x /usr/sbin/tailscaled ]; then
        "$BIN" tailscale backend system --data-dir "$DATA" >/dev/null
        "$BIN" tailscale start --data-dir "$DATA" || say "warning: could not start tailscale; see the Tailscale page in the console"
    elif [ "$TAILSCALE" = yes ]; then
        if "$BIN" tailscale install --data-dir "$DATA"; then
            "$BIN" tailscale backend system --data-dir "$DATA" >/dev/null
        else
            say "warning: installing tailscale failed; you can retry from the Tailscale page, or rerun with --no-tailscale"
        fi
    else
        "$BIN" tailscale backend embedded --data-dir "$DATA" >/dev/null
        say "Using the built-in Tailscale (no system tailscale)"
    fi

    if [ -n "${RSAGENT_AUTH_KEY:-}" ]; then
        say "Joining the tailnet..."
        "$BIN" provision --data-dir "$DATA" --auth-key "$RSAGENT_AUTH_KEY" \
            --tags "${RSAGENT_TAGS:-tag:serial-agent}" --hostname "${RSAGENT_HOSTNAME:-rs-$(cat /proc/sys/kernel/hostname | tr 'A-Z' 'a-z')}" \
            || say "warning: joining the tailnet failed; log in from the Tailscale page"
    fi

    "$BIN" service install --data-dir "$DATA"
    say "Service started: /etc/init.d/rsagent (logs in /tmp/rsagent/logs)"
    say "Console: http://<tailscale IP>:7890/ from the tailnet, or over SSH: ssh -L 7890:127.0.0.1:7890 root@<router>"
    exit 0
fi

# ---------------------------------------------------------------- Linux（systemd）
UNIT=/etc/systemd/system/rsagent.service

# 停掉旧服务再替换文件
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet rsagent 2>/dev/null; then
    systemctl stop rsagent
fi
install -m 755 "$TMP/rsagent" "$BIN"
mkdir -p "$DATA"
chmod 700 "$DATA"
say "Installed $("$BIN" version)"

if [ -n "${RSAGENT_AUTH_KEY:-}" ]; then
    say "Joining the tailnet..."
    "$BIN" provision --data-dir "$DATA" --auth-key "$RSAGENT_AUTH_KEY" \
        --tags "${RSAGENT_TAGS:-tag:serial-agent}" --hostname "${RSAGENT_HOSTNAME:-rs-$(hostname | tr 'A-Z' 'a-z')}"
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    cat > "$UNIT" <<EOF
[Unit]
Description=RemoteSerial Agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=-/etc/default/rsagent
ExecStart=$BIN run --data-dir $DATA
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable rsagent >/dev/null 2>&1
    systemctl restart rsagent
    say "Service started: systemctl status rsagent; console at http://127.0.0.1:7890/ (reachable via SSH port forwarding)"
else
    say "systemd not found; run it manually: $BIN run --data-dir $DATA"
fi
