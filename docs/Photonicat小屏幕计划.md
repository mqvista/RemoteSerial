# Photonicat 2 小屏幕显示 rsagent 状态

- 文档版本：v0.1，2026-11-09
- 状态：已实现，在 OpenWrt 24.10 容器中以模拟的屏幕程序测试（待 Photonicat 2 实机确认，见第 7 节）

## 1. 目标

在 Photonicat 2 机身的小屏幕（172×320，按键翻页）上增加一页「RemoteSerial」，显示 Tailscale 与串口桥接的状态。
不改动原厂的页面；移除后屏幕恢复原样。

## 2. 屏幕程序（原厂 pcat2-display-mini）

源码：https://github.com/photonicat/photonicat2_mini_display

| 项目 | 内容 |
|---|---|
| 服务 | `/etc/init.d/pcat2-display-mini`（套件 `pcat2-display-mini`） |
| 本机接口 | `127.0.0.1:8081`，只接受本机连接、不需要登录 |
| 用户配置 | `/etc/pcat2_mini_display-user_config.json`，深度合并到内置默认配置之上 |
| 页面 | `display_template.elements.page0 … pageN`；同名页面整页覆盖默认页；页数 = 页面个数 |
| GPS 页 | 默认的最后一页（page4）。程序把「最后一页且含 `Gps*` 数据键」当作 GPS 页，没有 GPS 时隐藏它 |
| 自定义数据 | `custom_metrics.sources`：`command`、`http_poll`、`http_endpoint`、`env`、`json_file` |

数据获取选用 `json_file`：一个来源用 `mappings` 对应多个数据键，按间隔读一个 JSON 文件，不需要启动进程也不走网络；
读取失败时屏幕显示 `FILE_ERROR`。UCI 不适合放即时数据（写 `/etc/config` 会磨损闪存）；`command` 与 `http_poll` 一个来源只能对应一个数据键。

## 3. rsagent 侧：状态文件（通用功能）

配置项 `statusFile`（默认空，不写）。设置后每 2 秒把精简状态写到该文件（先写临时文件再改名，读的一方不会读到半个文件）。
Photonicat 使用 `/tmp/rsagent/screen.json`（内存，不磨损闪存）。

值都是格式化好的短字符串（屏幕字体不一定支持中文，状态用英文短词）：

| 字段 | 示例 | 说明 |
|---|---|---|
| `tsState` | `Online` / `Login` / `Offline` / `No TS` | Tailscale 状态 |
| `tsIP` | `100.101.102.103` | |
| `tsName` | `rs-router` | MagicDNS 名称的第一段 |
| `bridges` | `2/3` | 有客户端连接的桥接数 / 启用的桥接数 |
| `client` | `you@laptop` | 第一个已连接的客户端，没有则 `-` |
| `rxRate` / `txRate` | `11.3 KB/s` | 所有桥接合计：串口 → TCP / TCP → 串口 |
| `console` | `7890` | 管理界面端口 |
| `version` | `0.6.1` | |

## 4. 屏幕页面

一页，沿用原厂第 4 页的排版（`unit` 字体，每行 23 像素，标签黄色、数值白色）：

```
RemoteSerial
TS:      Online
100.101.102.103
rs-router
Bridges:     2/3
Client:
you@laptop
RX:    11.3 KB/s
TX:       0 B/s
Console:    7890
```

数据键统一加 `RS` 前缀（`RSTsState`、`RSTsIP` …），不与原厂数据键冲突；页面首个元素的 `label` 为 `rsagent-page`，用于识别。

## 5. 安装与移除：`rsagent screen photonicat install | remove | status`

由 Go 实现（比在 shell 里改 JSON 可靠），套件的安装/移除脚本调用它。

安装：
1. 确认有屏幕程序；从 `127.0.0.1:8081` 读取合并后的配置与默认配置。
2. 若已有 RemoteSerial 页，原地更新；否则插在 GPS 页之前（没有 GPS 页时放在最后），其后的页面顺延一位写入用户配置。
3. `custom_metrics.sources` 中加入名为 `RemoteSerial` 的 `json_file` 来源（已有则替换）。
4. 首次修改前备份用户配置到 `/etc/rsagent/pcat2_mini_display-user_config.backup.json`。
5. 原子写入用户配置，重启屏幕程序；把 rsagent 的 `statusFile` 设为 `/tmp/rsagent/screen.json`。

移除：找到 RemoteSerial 页，后面的页面前移一位；与默认页面完全相同的页面从用户配置中删除（以后固件更新的默认页面才能生效）；
删除 `RemoteSerial` 来源；重启屏幕程序；关闭 `statusFile`。

## 6. 套件 `rsagent-photonicat`

- 不分架构（`all` / `noarch`），依赖 `rsagent`、`pcat2-display-mini`；内容只有安装/移除脚本与一个标记文件。
- 安装后执行 `rsagent screen photonicat install`，移除前执行 `rsagent screen photonicat remove`。
- `install.sh` 检测到 `pcat2-display-mini` 时一并安装；一键更新时若已安装也一并更新。
- LuCI 页面检测到 Photonicat 屏幕时显示状态，未安装时提示安装方法。

## 7. 待实机确认

1. 在原厂网页修改屏幕设置并保存后，RemoteSerial 页与 `json_file` 来源是否保留。
2. 页面在实际屏幕上的排版（字体宽度、是否超出 172 像素）。
3. GPS 页启用时，翻页顺序是否为 … → RemoteSerial → GPS。
