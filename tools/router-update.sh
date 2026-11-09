#!/bin/sh
# 开发用：编译 OpenWrt 测试套件，传到路由器并安装（一条命令完成）。
#
#   tools/router-update.sh root@192.168.100.1            # 编译并安装
#   tools/router-update.sh root@192.168.100.1 --no-build # 用 dist/ 里已有的套件
#   make router ROUTER=root@192.168.100.1
#
# 版本号自动递增：最新标签的下一个修订号加时间，如 0.6.1-dev.2611101530（套件为 0.6.1~pre2611101530-r1），
# 每次都比上一次新，opkg / apk 才会真的安装。只上传这台路由器需要的套件：rsagent，
# 装有 LuCI 时加 luci-app-rsagent，有 Photonicat 小屏幕时加 rsagent-photonicat。
# SSH 连接复用，密码最多问一次。额外的 ssh 选项可放在 RSAGENT_SSH_OPTS（如 "-i ~/.ssh/router"）。
set -eu

TARGET=${1:-}
[ -n "$TARGET" ] || { echo "usage: $0 root@<router> [--no-build]" >&2; exit 2; }
BUILD=yes
[ "${2:-}" = "--no-build" ] && BUILD=no

cd "$(dirname "$0")/.."
export PATH="/opt/homebrew/bin:$HOME/.orbstack/bin:$PATH"

CTL="/tmp/rsagent-ssh-$$"
SSHOPT="-o ControlMaster=auto -o ControlPath=$CTL -o ControlPersist=120 ${RSAGENT_SSH_OPTS:-}"
cleanup() { ssh $SSHOPT -O exit "$TARGET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
rsh() { ssh $SSHOPT "$TARGET" "$@"; }

echo "==> Checking $TARGET"
INFO=$(rsh '
	if [ -d /lib/apk/db ] && command -v apk >/dev/null 2>&1; then echo "pm=apk"; echo "arch=$(cat /etc/apk/arch 2>/dev/null)";
	else echo "pm=opkg"; echo "arch=$(opkg print-architecture | awk "\$1==\"arch\"{a=\$2} END{print a}")"; fi
	[ -d /www/luci-static ] && echo luci=yes || echo luci=no
	[ -x /etc/init.d/pcat2-display-mini ] && echo pcat=yes || echo pcat=no')
PM=$(echo "$INFO" | sed -n 's/^pm=//p')
RARCH=$(echo "$INFO" | sed -n 's/^arch=//p')
LUCI=$(echo "$INFO" | sed -n 's/^luci=//p')
PCAT=$(echo "$INFO" | sed -n 's/^pcat=//p')
case "$RARCH" in
	x86_64) ARCH=x86_64 ;;
	aarch64*) ARCH=aarch64_generic ;;
	*) echo "error: no package for router architecture '$RARCH'" >&2; exit 1 ;;
esac
echo "    $PM, $RARCH, LuCI: $LUCI, Photonicat screen: $PCAT"

if [ "$BUILD" = yes ]; then
	TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)
	BASE=${TAG#v}
	V="${BASE%.*}.$(( ${BASE##*.} + 1 ))-dev.$(date +%y%m%d%H%M)"
	echo "==> Building $V"
	NOAPK=""
	[ "$PM" = opkg ] && NOAPK="NO_APK=1" # .ipk 不需要 docker，快一些
	make web openwrt-pkg VERSION="$V" $NOAPK >/tmp/rsagent-router-build.log 2>&1 \
		|| { echo "error: build failed, see /tmp/rsagent-router-build.log" >&2; exit 1; }
fi

if [ "$PM" = opkg ]; then
	pick() { ls -t dist/$1_*_$2.ipk 2>/dev/null | head -1; }
else
	pick() { ls -t dist/$1-*_$2.apk 2>/dev/null | head -1; }
fi
FILES=$(pick rsagent "$ARCH")
[ -n "$FILES" ] || { echo "error: no rsagent package for $ARCH in dist/" >&2; exit 1; }
[ "$LUCI" = yes ] && FILES="$FILES $(pick luci-app-rsagent all)"
[ "$PCAT" = yes ] && FILES="$FILES $(pick rsagent-photonicat all)"

echo "==> Uploading"
for f in $FILES; do echo "    $(basename "$f")"; done
rsh 'rm -rf /tmp/rsagent-up && mkdir -p /tmp/rsagent-up'
# shellcheck disable=SC2086
scp -O $SSHOPT -q $FILES "$TARGET:/tmp/rsagent-up/"

echo "==> Installing"
if [ "$PM" = opkg ]; then
	rsh 'opkg install /tmp/rsagent-up/*.ipk 2>&1 | grep -vE "^20[0-9]{2}/"; rm -rf /tmp/rsagent-up'
else
	rsh 'apk add --allow-untrusted /tmp/rsagent-up/*.apk 2>&1 | grep -vE "^ *\* 20[0-9]{2}/"; rm -rf /tmp/rsagent-up'
fi
rsh 'sleep 2; rsagent version'
echo "==> Done. Reload the LuCI / console page with Ctrl+Shift+R (Cmd+Shift+R on macOS)."
