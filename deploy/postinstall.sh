#!/bin/sh
# deb 安装后：注册并启动 systemd 服务。
set -e
mkdir -p /var/lib/rsagent
chmod 700 /var/lib/rsagent
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    systemctl enable rsagent >/dev/null 2>&1 || true
    if systemctl is-active --quiet rsagent; then
        systemctl restart rsagent || true
    else
        systemctl start rsagent || true
    fi
fi
exit 0
