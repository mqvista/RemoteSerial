# rsagent 远程串口桥接 Agent

在异地 PC 上运行的单文件程序：内嵌 Tailscale 节点，把本机串口转成 tailnet 内可达的 TCP 服务，带 web 管理界面、托盘和 webview 窗口，可作为子网路由。

设计与计划见 [docs/开发方案与计划.md](docs/开发方案与计划.md)。

## 开发环境

- Go 1.27+
- Node 20+（前端构建）

## 构建

```powershell
# Windows
.\build.ps1                 # 前端 + windows/amd64 + linux/arm64，输出到 dist/
.\build.ps1 -SkipWeb        # 跳过前端
```

```sh
# Linux / macOS
make all
```

## 本地运行

```powershell
# 前端开发服务器（代理 /api 到 core）
cd web; npm install; npm run dev

# core，数据目录指到仓库外避免污染 ProgramData
go run ./cmd/rsagent run --console --data-dir $env:TEMP\rsagent-dev
```

浏览器打开 `http://127.0.0.1:7890/`。未构建前端时显示提示页，API 仍可用。

## 目录

```
cmd/rsagent        入口与子命令
internal/app       组件装配
internal/config    配置文件
internal/logging   日志：滚动文件 + 内存环
internal/web       HTTP API 与内嵌前端
web/               Vue 3 + Vite 前端
deploy/            ACL 模板、部署辅助文件
docs/              设计文档
```
