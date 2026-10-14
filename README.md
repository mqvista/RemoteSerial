# rsagent 远程串口桥接 Agent

在异地 PC 上运行的单文件程序：内嵌 Tailscale 节点，把本机串口转成 tailnet 内可达的 TCP 服务，带 web 管理界面、托盘和 webview 窗口，可作为子网路由。

设计与计划见 [docs/开发方案与计划.md](docs/开发方案与计划.md)。

## 使用

1. 在 Tailscale 后台生成 auth key：reusable、pre-authorized、带 tag（如 `tag:serial-agent`）。带 tag 的节点不会因密钥过期掉线。
2. 把 `rsagent-windows-amd64.exe` 复制到远端 PC，双击运行。托盘出现图标，窗口打开管理界面。
3. 在"Tailscale"页输入 auth key 登录。登录状态会保存，重启后自动上线，auth key 本身不会保存。
4. 在"串口桥接"页新增桥接：选串口、波特率、TCP 端口、模式（裸 TCP 或 RFC2217）。
5. 本地任意 tailnet 设备访问 `http://<节点名>/` 管理，"终端"页直接在浏览器里看串口；或用 `rfc2217://<节点IP>:<端口>`、裸 TCP 连接。

ACL 需要允许你的账号访问 tag 节点的 80 端口和桥接端口，模板见 `deploy/acl.example.json`。若两个 tag 节点之间也要互通（例如用 tsprobe 联调），ACL 还要放行 tag 到 tag。

## 命令

```
rsagent              无参数等于 ui（仅 Windows）
rsagent ui           托盘 + 窗口；本机已有 core 时只唤起窗口或只跑壳
rsagent run          前台运行 core，不带界面，适合 Linux 与 systemd
rsagent version
```

通用选项：`--data-dir`、`--listen`（默认 127.0.0.1:7890）、`--log-level`；`run` 另有 `--console`。

数据目录默认 Windows 为 `%ProgramData%\RemoteSerial`，Linux root 为 `/var/lib/rsagent`，内含 `config.json`、`tsstate\`（节点私钥，勿外泄）、`logs\`。

## 开发环境

- Go 1.27+
- Node 20+（前端构建）

## 构建

```powershell
# Windows
.\build.ps1                 # 前端 + windows/amd64（GUI 子系统）+ linux/arm64，输出到 dist/
.\build.ps1 -SkipWeb        # 跳过前端
```

```sh
# Linux / macOS
make all
```

## 本地运行

```powershell
# 前端开发服务器（代理 /api 和 /ws 到 core）
cd web; npm install; npm run dev

# core，数据目录指到仓库外避免污染 ProgramData
go run ./cmd/rsagent run --console --data-dir $env:TEMP\rsagent-dev
```

浏览器打开 `http://127.0.0.1:7890/`。未构建前端时显示提示页，API 仍可用。

### 联调工具 tsprobe

用一个临时 tsnet 节点从 tailnet 侧访问 agent，不依赖本机官方客户端：

```powershell
go build -o dist/tsprobe.exe ./cmd/tsprobe
.\dist\tsprobe.exe -authkey <key> -get http://100.x.y.z/api/status
.\dist\tsprobe.exe -authkey <key> -tcp 100.x.y.z:2217 -send "hello\r\n" -wait 5s
```

它会先做 disco 和 TSMP ping 并打印对端在 netmap 中的状态，便于区分"没有路径"和"ACL 拦截"。

## 目录

```
cmd/rsagent        入口与子命令
cmd/tsprobe        联调探测工具
internal/app       组件装配：Run（core）、RunUI（桌面模式）
internal/config    配置文件
internal/logging   日志：滚动文件 + 内存环
internal/tsnode    tsnet 节点封装
internal/bridge    串口桥接
internal/web       HTTP API、WebSocket 推送、内嵌前端
internal/shell     托盘 + webview（Windows）
internal/deploy    部署检测与动作：计划任务、服务、防火墙、WebView2、提权子进程
tools/mkicon       图标生成
web/               Vue 3 + Vite 前端
deploy/            ACL 模板、部署辅助文件
docs/              设计文档
```
