#!/bin/sh
# deb 卸载前：停止并取消开机启动。数据目录 /var/lib/rsagent 保留。
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop rsagent >/dev/null 2>&1 || true
    systemctl disable rsagent >/dev/null 2>&1 || true
fi
exit 0
