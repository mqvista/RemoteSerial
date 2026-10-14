#!/bin/sh
# rsagent Linux 一键安装脚本：下载对应架构的程序，注册 systemd 服务，可选自动入网。
#
#   curl -fsSL https://raw.githubusercontent.com/mqvista/RemoteSerial/main/deploy/install.sh | sudo sh
#   curl -fsSL .../install.sh | sudo RSAGENT_AUTH_KEY=tskey-client-xxx RSAGENT_TAGS=tag:serial-agent sh
#
# 环境变量：
#   RSAGENT_VERSION    指定版本（如 0.4.0），默认最新
#   RSAGENT_BASE_URL   下载地址前缀，默认 GitHub Releases；私有仓库或内网改为自建地址
#   RSAGENT_FROM_FILE  跳过下载，直接安装本地程序文件
#   RSAGENT_AUTH_KEY   auth key 或 OAuth client secret，给了就在启动前入网
#   RSAGENT_TAGS       入网 tag，默认 tag:serial-agent
#   RSAGENT_HOSTNAME   节点名，默认 rs-<主机名>
set -eu

REPO="mqvista/RemoteSerial"
BIN=/usr/bin/rsagent
DATA=/var/lib/rsagent
UNIT=/etc/systemd/system/rsagent.service

say() { printf '%s\n' "$*"; }
die() { say "错误: $*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "请用 root 运行（sudo）"

case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "不支持的架构 $(uname -m)，只提供 amd64 和 arm64" ;;
esac

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if [ -n "${RSAGENT_FROM_FILE:-}" ]; then
    cp "$RSAGENT_FROM_FILE" "$TMP/rsagent"
else
    command -v curl >/dev/null 2>&1 || die "需要 curl"
    if [ -n "${RSAGENT_BASE_URL:-}" ]; then
        BASE=${RSAGENT_BASE_URL%/}
    elif [ -n "${RSAGENT_VERSION:-}" ]; then
        BASE="https://github.com/$REPO/releases/download/v${RSAGENT_VERSION#v}"
    else
        BASE="https://github.com/$REPO/releases/latest/download"
    fi
    say "下载 $BASE/rsagent-linux-$ARCH"
    curl -fsSL -o "$TMP/rsagent" "$BASE/rsagent-linux-$ARCH"
    if curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" 2>/dev/null; then
        want=$(grep " rsagent-linux-$ARCH\$" "$TMP/SHA256SUMS" | awk '{print $1}')
        got=$(sha256sum "$TMP/rsagent" | awk '{print $1}')
        [ -n "$want" ] && [ "$want" != "$got" ] && die "SHA-256 校验失败"
        say "SHA-256 校验通过"
    fi
fi
chmod 755 "$TMP/rsagent"
"$TMP/rsagent" version >/dev/null || die "程序无法运行"

# 停掉旧服务再替换文件
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet rsagent 2>/dev/null; then
    systemctl stop rsagent
fi
install -m 755 "$TMP/rsagent" "$BIN"
mkdir -p "$DATA"
chmod 700 "$DATA"
say "已安装 $("$BIN" version)"

if [ -n "${RSAGENT_AUTH_KEY:-}" ]; then
    say "正在入网…"
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
    say "服务已启动：systemctl status rsagent；管理界面 http://127.0.0.1:7890/（可用 SSH 端口转发访问）"
else
    say "没有 systemd，请手动运行：$BIN run --data-dir $DATA"
fi
