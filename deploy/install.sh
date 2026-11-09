#!/bin/sh
# rsagent 一键安装脚本（Linux 与 OpenWrt）：下载对应架构的程序，注册系统服务，可选自动入网。
#
#   curl -fsSL https://raw.githubusercontent.com/mqvista/RemoteSerial/main/deploy/install.sh | sudo sh
#   curl -fsSL .../install.sh | sudo RSAGENT_AUTH_KEY=tskey-client-xxx RSAGENT_TAGS=tag:serial-agent sh
#
# OpenWrt（以 root 登录，没有 curl 时用 wget）：
#   wget -qO- https://raw.githubusercontent.com/mqvista/RemoteSerial/main/deploy/install.sh | sh
#   离线：把 rsagent-openwrt-<arch>.tar.gz 解压后执行 sh rsagent-openwrt/install.sh
#
# OpenWrt 在线安装默认使用套件（24.10 为 .ipk，25.x 为 .apk）：tailscale 与常见 USB 串口驱动随依赖自动安装，
# 之后可用 opkg / apk 移除；再次执行本脚本即升级到最新版。架构没有对应套件时（或离线包、--no-package）
# 改为直接安装文件。
#
# 参数（OpenWrt）：
#   --with-tailscale   系统没有 tailscale 时直接安装，不询问（直接安装文件时）
#   --no-tailscale     不安装系统 tailscale，改用 rsagent 内置的 Tailscale（直接安装文件）
#   --no-luci          不安装 LuCI 页面（服务 → RemoteSerial）
#   --no-package       不用套件，直接安装文件
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
LUCI=yes
PACKAGE=yes
for a in "$@"; do
    case "$a" in
        --with-tailscale) TAILSCALE=yes ;;
        --no-tailscale) TAILSCALE=no ;;
        --no-luci) LUCI=no ;;
        --no-package) PACKAGE=no ;;
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

BASE=""
if [ -z "${RSAGENT_FROM_FILE:-}" ]; then
    if [ -n "${RSAGENT_BASE_URL:-}" ]; then
        BASE=${RSAGENT_BASE_URL%/}
    elif [ -n "${RSAGENT_VERSION:-}" ]; then
        BASE="https://github.com/$REPO/releases/download/v${RSAGENT_VERSION#v}"
    else
        BASE="https://github.com/$REPO/releases/latest/download"
    fi
fi

# verify <file> <name>：按发布的 SHA256SUMS 校验（取不到校验和时跳过）
SUMS=""
verify() {
    if [ -z "$SUMS" ]; then
        SUMS=none
        fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS" 2>/dev/null && SUMS="$TMP/SHA256SUMS"
    fi
    [ "$SUMS" = none ] && return 0
    want=$(grep " $2\$" "$SUMS" | awk '{print $1}')
    got=$(sha256sum "$1" | awk '{print $1}')
    [ -n "$want" ] && [ "$want" != "$got" ] && die "SHA-256 mismatch for $2"
    [ -n "$want" ] && say "SHA-256 verified: $2"
    return 0
}

# ---------------------------------------------------------------- OpenWrt 套件
# 下载并安装 .ipk / .apk。返回 1 表示这台路由器不适用套件（架构不符、找不到套件），改为直接安装文件。
install_packages() {
    if [ -d /lib/apk/db ] && command -v apk >/dev/null 2>&1; then
        PM=apk
    elif command -v opkg >/dev/null 2>&1; then
        PM=opkg
    else
        return 1
    fi
    case "$ARCH" in amd64) PA=x86_64 ;; arm64) PA=aarch64_generic ;; esac
    if [ "$PM" = opkg ]; then
        opkg print-architecture | grep -q " $PA " || { say "No package for this router's architecture; installing files instead"; return 1; }
    else
        # OpenWrt 的套件架构写在 /etc/apk/arch（如 aarch64_generic），apk --print-arch 只给出 aarch64
        PARCHES=$(cat /etc/apk/arch 2>/dev/null || apk --print-arch)
        printf '%s\n' $PARCHES | grep -qx "$PA" || { say "No package for architecture $PARCHES; installing files instead"; return 1; }
    fi
    if [ -n "${RSAGENT_VERSION:-}" ]; then
        V=${RSAGENT_VERSION#v}
    else
        fetch "$BASE/manifest.json" "$TMP/manifest.json" 2>/dev/null || return 1
        V=$(jsonfilter -i "$TMP/manifest.json" -e '@.version' 2>/dev/null || true)
    fi
    case "$V" in ''|*-*) return 1 ;; esac
    if [ "$PM" = opkg ]; then
        FILES="rsagent_${V}-r1_${PA}.ipk"
        LUCIPKG="luci-app-rsagent_${V}-r1_all.ipk"
    else
        FILES="rsagent-${V}-r1_${PA}.apk"
        LUCIPKG="luci-app-rsagent-${V}-r1_all.apk"
    fi
    [ "$LUCI" = yes ] && [ -d /www/luci-static ] && FILES="$FILES $LUCIPKG"
    LOCAL=""
    for f in $FILES; do
        say "Downloading $BASE/$f"
        fetch "$BASE/$f" "$TMP/$f" 2>/dev/null || { say "Package $f not found; installing files instead"; return 1; }
        verify "$TMP/$f" "$f"
        LOCAL="$LOCAL $TMP/$f"
    done
    say "Installing rsagent $V with $PM (dependencies: tailscale, USB serial drivers)"
    # 以前直接安装文件时的服务先停掉，文件交给套件
    [ -x /etc/init.d/rsagent ] && /etc/init.d/rsagent stop >/dev/null 2>&1 || true
    if [ "$PM" = opkg ]; then
        opkg update >/dev/null || die "opkg update failed; check the router's internet connection"
        # shellcheck disable=SC2086
        opkg install $LOCAL || die "opkg install failed"
    else
        apk update >/dev/null || die "apk update failed; check the router's internet connection"
        # shellcheck disable=SC2086
        # tailscale 明确加入 world：之后移除 rsagent 时不会把路由器的 Tailscale 一起移除
        apk add --allow-untrusted tailscale $LOCAL || die "apk add failed"
    fi
    # 已是同一版本或更新的版本时套件管理器什么都不做，不会执行安装后脚本：服务要自己重新启动
    /etc/init.d/rsagent enable >/dev/null 2>&1 || true
    /etc/init.d/rsagent start >/dev/null 2>&1 || true
    case "$("$BIN" version 2>/dev/null)" in
        "rsagent $V "*) ;;
        *) say "note: rsagent $V was not installed because the installed package is the same or newer" ;;
    esac
    return 0
}

if [ "$OPENWRT" = 1 ] && [ -z "${RSAGENT_FROM_FILE:-}" ] && [ "$PACKAGE" = yes ] && [ "$TAILSCALE" != no ] && install_packages; then
    say "Installed $("$BIN" version)"
    if [ -n "${RSAGENT_AUTH_KEY:-}" ]; then
        say "Joining the tailnet..."
        i=0
        until "$BIN" ctl status 2>/dev/null | grep -q '"running": *true' || [ $i -ge 15 ]; do sleep 1; i=$((i + 1)); done
        printf '%s\n' "$RSAGENT_AUTH_KEY" | "$BIN" ctl tailscale-authkey --tags "${RSAGENT_TAGS:-tag:serial-agent}" >/dev/null \
            || say "warning: joining the tailnet failed; log in from LuCI or the console"
    fi
    say "Service started: /etc/init.d/rsagent (logs in /tmp/rsagent/logs)"
    if [ -d /www/luci-static ] && [ "$LUCI" = yes ]; then
        say "Console: open it from LuCI (Services -> RemoteSerial), or http://<tailscale IP>:7890/ from the tailnet"
    else
        say "Console: http://<tailscale IP>:7890/ from the tailnet, or over SSH: ssh -L 7890:127.0.0.1:7890 root@<router>"
    fi
    say "Upgrade: run this script again. Remove: $PM $( [ "$PM" = opkg ] && echo remove || echo del ) luci-app-rsagent rsagent"
    exit 0
fi

if [ -n "${RSAGENT_FROM_FILE:-}" ]; then
    say "Using local file $RSAGENT_FROM_FILE"
    cp "$RSAGENT_FROM_FILE" "$TMP/rsagent"
else
    say "Downloading $BASE/rsagent-linux-$ARCH"
    fetch "$BASE/rsagent-linux-$ARCH" "$TMP/rsagent"
    verify "$TMP/rsagent" "rsagent-linux-$ARCH"
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
        if (exec </dev/tty) 2>/dev/null; then
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

    # LuCI 页面：离线包里自带 luci/，在线安装时从同一处下载（找不到就跳过）
    LUCI_OK=0
    if [ "$LUCI" = yes ] && [ -d /www/luci-static ]; then
        SRC=""
        if [ -n "$HERE" ] && [ -d "$HERE/luci" ]; then
            SRC="$HERE/luci"
        elif [ -n "$BASE" ] && fetch "$BASE/luci-app-rsagent.tar.gz" "$TMP/luci.tar.gz" 2>/dev/null; then
            tar -xzf "$TMP/luci.tar.gz" -C "$TMP" && SRC="$TMP/luci"
        fi
        if [ -n "$SRC" ]; then
            cp -R "$SRC/." /
            chmod 755 /usr/libexec/rpcd/luci.rsagent
            rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache
            /etc/init.d/rpcd reload >/dev/null 2>&1 || true
            LUCI_OK=1
            say "Installed the LuCI page: Services -> RemoteSerial"
        else
            say "LuCI page not available for this install; skipped"
        fi
    fi
    if [ "$LUCI_OK" = 1 ]; then
        say "Console: open it from LuCI (Services -> RemoteSerial), or http://<tailscale IP>:7890/ from the tailnet"
    else
        say "Console: http://<tailscale IP>:7890/ from the tailnet, or over SSH: ssh -L 7890:127.0.0.1:7890 root@<router>"
    fi
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
