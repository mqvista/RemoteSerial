# rsagent OpenWrt 版本计划

- 文档版本：v0.2，2026-11-09
- v0.2 变更：LuCI 可修改控制台端口（3.4、4.1）；新增系统 Tailscale 未安装/未运行/未登录时的检测与引导（3.5）。
- 状态：阶段 1–3 已实现并在 24.10 / 25.12 容器中测试（待实机验证）
- 目标硬件：x86_64 软路由、RK3568（aarch64）
- 目标系统：OpenWrt 24.10（opkg / `.ipk`）与 OpenWrt 25.x（apk v3 / `.apk`）

---

## 1. 目标与范围

在 OpenWrt 路由器上运行 rsagent，把接在路由器 USB 上的串口暴露到 tailnet，功能与 Linux 版一致（桥接、RFC2217、监看/终端、录制、日志）。

与现有版本的主要区别：

1. **网络使用系统自带的 Tailscale**（OpenWrt 的 `tailscale` 套件），rsagent 不再内嵌第二个节点。
2. **两套界面**：LuCI 里的精简管理页 + rsagent 自己的完整控制台。
3. **同时提供 `.ipk` 与 `.apk` 套件**。

不在本期范围：MIPS / 16MB 闪存的小型路由器（当前程序约 22–25MB，见附录 A）。

## 2. 关键决定

| 问题 | 决定 | 理由 |
|---|---|---|
| Tailscale 来源 | 使用系统 tailscaled，通过本机 LocalAPI 控制 | 路由器上本来就跑 tailscale；内核转发比 tsnet 用户态快；不会出现同一台设备两个节点 |
| 程序是否单独编译 | **不单独编译**，沿用 `rsagent-linux-amd64/arm64`，运行时识别 OpenWrt | 静态编译不依赖 libc；一份程序修一次全平台受益 |
| 配置以谁为准 | rsagent 自己的 `config.json` 与 API 是唯一来源，LuCI 只做薄客户端 | 避免 UCI 与 config.json 双写不一致，也避免两套表单、两套翻译 |
| LuCI 页做到什么程度 | 服务启停、监听设置、桥接列表与开关、一键进入控制台 | 桥接编辑、终端、录制等复杂功能只在自己的控制台做一次 |
| 套件格式 | 一份 OpenWrt 套件 Makefile，分别用 24.10 SDK 出 `.ipk`、25.x SDK 出 `.apk` | 官方工具链产物最可靠；apk v3 与 Alpine apk 格式不同，nfpm 产不出 |
| 程序内自更新 | OpenWrt 上默认关闭，改为提示用套件管理器升级 | `/usr/bin/rsagent` 归套件管理器所有，自行替换会让套件数据库与实际文件不一致 |

## 3. 架构

```
            tailnet
               │
     ┌─────────┴──────────┐
     │ tailscaled（系统） │  tailscale0 / 100.x.y.z
     └─────────┬──────────┘
               │ LocalAPI：/var/run/tailscale/tailscaled.sock
┌──────────────┴───────────────────────────────┐
│ rsagent（procd 服务，root）                   │
│  - 桥接：监听 100.x.y.z:<端口>                │
│  - 控制台：100.x.y.z:7890，可选 LAN:7890      │
│  - 本机 API：127.0.0.1:7890                   │
└──────────────┬───────────────────────────────┘
               │ rsagent ctl … --json（走本机 API）
┌──────────────┴──────────────┐
│ rpcd 插件 luci.rsagent       │ ← ubus，受 LuCI 登录与 ACL 保护
└──────────────┬──────────────┘
               │
         LuCI 页面（浏览器）
```

### 3.1 网络后端：增加“系统 Tailscale”模式

现有 `internal/tsnode` 内部已经通过 `tailscale.com/client/local` 的 `local.Client` 与 tsnet 交互（监听 IPN 总线、WhoIs、Peers、路由），这部分可以几乎原样指向系统 tailscaled 的 socket。

新增配置项 `tailscale.backend`：`embedded`（默认，现有行为）| `system`（OpenWrt 默认）。

| 能力 | embedded（现有） | system（新增） |
|---|---|---|
| 状态监听 | tsnet 的 local.Client | `local.Client{Socket: "/var/run/tailscale/tailscaled.sock"}`，代码复用 |
| 监听端口 | `tsnet.Server.Listen` | `net.Listen("tcp", "100.x.y.z:端口")`；IP 变化时重新绑定 |
| 来访身份 / 允许的登录 | tsnet WhoIs | LocalAPI WhoIs，`Access.AllowedLogins` 照常生效 |
| 登录 | tsnet + auth key | LocalAPI `Start`（带 AuthKey）+ 登录流程；也可直接用 luci-app-tailscale 登录 |
| 子网路由 | 写入 tsnet 偏好 | LocalAPI `EditPrefs`；**只增删 rsagent 自己加的路由**，保留用户在别处设的 |
| 标签、主机名 | tsnet 配置 | LocalAPI `EditPrefs`；默认只显示，不主动修改 |
| Ping / Netcheck | 现有实现 | 改走 LocalAPI；如有不支持的项在界面隐藏 |
| 登出 | 清除 tsnet 状态 | 系统模式下**不提供**（会影响路由器上的其他用途），只给提示 |

注意事项：

- **端口 80**：uhttpd（LuCI）通常监听 `0.0.0.0:80`，Linux 上再绑定 `100.x.y.z:80` 会冲突。系统模式下 tailnet 侧控制台改用 `7890`（可配置）。
- **启动顺序**：rsagent 可能先于 tailscaled 就绪，需要等待 socket 与 IP 出现，期间桥接显示“等待网络”（现有状态即可表达）。
- **LocalAPI 版本**：OpenWrt 套件里的 tailscale 版本可能比我们依赖的 `tailscale.com` 模块旧。需实测 24.10 / 25.x 自带版本，必要时对缺失的接口降级处理。
- **构建体积**：系统模式不需要 tsnet，但为了“一份程序”暂不拆 build tag；以后如需更小的程序，可再加 `notsnet` 标签。

### 3.2 识别 OpenWrt 与路径

启动时检测 `/etc/openwrt_release`，存在即视为 OpenWrt：

| 项目 | 普通 Linux | OpenWrt |
|---|---|---|
| 配置与节点状态 | `/var/lib/rsagent` | `/etc/rsagent`（写入 `/lib/upgrade/keep.d/rsagent`，升级固件后保留） |
| 日志 | 数据目录 `logs/` | `/tmp/rsagent/logs`（内存，避免磨损闪存）+ 重要事件写 syslog（`logread` 可见） |
| 录制文件 | 数据目录 | `/tmp/rsagent/captures`，可在设置中改到 USB 存储 |
| 服务管理 | systemd | procd |
| 网络后端默认值 | embedded | system |

### 3.3 procd 服务与部署页

- `/etc/init.d/rsagent`：`USE_PROCD=1`，`procd_set_param respawn`，`START=99`（在网络与 tailscale 之后）。
- `rsagent service install/start/stop/status` 在 OpenWrt 上改为调用 `/etc/init.d/rsagent enable|start|stop|running`。
- 部署页：隐藏计划任务、WebView2、Windows 防火墙、macOS 登录项等项目；显示 procd 状态、tailscaled 是否运行、USB 串口驱动是否已加载、套件版本与升级命令。

### 3.4 本机控制台的访问与认证

| 来源 | 认证方式 |
|---|---|
| tailnet | 现有机制：WhoIs + 允许的登录 |
| 127.0.0.1 | 现有机制：本机信任 |
| LAN（可选，默认关闭） | 需要登录：从 LuCI 的“打开控制台”按钮带一次性令牌免登录，或设置独立密码 |

控制台端口默认 `7890`，可在 LuCI 或控制台的设置中修改（见 4.1），修改时：

1. 检查端口：1–65535、不与 uhttpd（80/443）、dropbear（22）、各桥接端口重复，且本机当前未被占用（`netstat`/尝试绑定）。
2. rsagent 先在新端口开始监听，成功后再关闭旧端口；失败则保持原端口并返回错误，不会把自己“锁在门外”。
3. 同步更新防火墙：rpcd 插件改写 uci 中 rsagent 的放行规则（`firewall.rsagent_console`）并 `reload`。
4. LuCI 的“打开控制台”按钮与 tailnet 侧地址随之使用新端口。

一次性令牌：LuCI 通过 rpcd 调用 `rsagent ctl token`，得到 60 秒有效、只能用一次的令牌，跳转到 `http://<路由器>:7890/?token=…`；rsagent 验证后发一个会话 Cookie。这样 LAN 侧访问复用路由器登录，不需要另记密码。

### 3.5 系统 Tailscale 未安装时的引导

系统模式依赖 tailscaled。rsagent 启动后与每次刷新状态时按顺序检测，并把结果作为 tailscale 状态显示在**控制台与 LuCI 两边**：

| 检测 | 状态 | 界面提示与一键操作 |
|---|---|---|
| `/usr/sbin/tailscaled` 不存在 | `NotInstalled` 未安装 | 说明原因 + **安装 Tailscale** 按钮 + 手动命令 |
| 已安装但 `/etc/init.d/tailscale` 未启用或未运行 | `NotRunning` 未运行 | **启动并设为开机启动** 按钮 |
| 运行中但 socket 不可连接 | `Error` | 显示错误，建议查看 `logread -e tailscale` |
| 已连接、未登录 | `NeedsLogin` 需要登录 | 输入 auth key 登录，或 **在浏览器中登录**（LocalAPI 交互登录，显示网址与二维码） |
| 已登录、等待管理员批准 | `NeedsMachineAuth` | 提示到 Tailscale 后台批准该设备 |
| 正常 | `Running` | 显示 IP 与主机名 |

**一键安装**（rsagent 以 root 运行，可直接执行；输出实时显示在页面上）：

1. 安装前检查：可连网（能解析套件源）、`/overlay` 剩余空间足够（按套件源给出的大小加余量），不足时直接说明并停止。
2. OpenWrt 24.10：`opkg update && opkg install tailscale`；25.x：`apk update && apk add tailscale`（按系统自动选择）。
3. `/etc/init.d/tailscale enable && /etc/init.d/tailscale start`。
4. 防火墙：确保 `tailscale0` 属于一个允许入站的区域（与套件安装时的 `uci-defaults` 相同逻辑，已有则跳过）。
5. 进入“需要登录”步骤。

**失败时的退路**：无法连网、空间不足或套件源里没有 tailscale 时，给出：

- 手动安装命令（复制即可）与离线安装说明（从另一台电脑下载套件后 `opkg install ./tailscale_*.ipk` / `apk add --allow-untrusted ./tailscale-*.apk`）。
- 切换到 **内嵌 Tailscale（embedded）** 模式的按钮：不依赖系统套件，代价是多占内存、子网路由较慢。

**其他引导渠道**：

- **套件依赖**：`.ipk`/`.apk` 安装 rsagent 时把 `tailscale` 设为依赖，用套件管理器安装时会自动装上；大多数用户根本不会遇到“未安装”。只有手动解压 tar 包或用户后来卸载了 tailscale 才需要上面的引导。
- **`install.sh`**：检测到没有 tailscale 时询问是否一并安装（`--with-tailscale` 跳过询问，`--no-tailscale` 则配置为 embedded 模式）。
- **命令行**：`rsagent ctl status` 与 `/etc/init.d/rsagent status` 在未安装时打印一行提示与安装命令；启动日志（`logread`）同样写一条。
- **文档**：在 `docs/使用说明.md` 新增“OpenWrt”一节：安装 rsagent、安装与登录 Tailscale、防火墙区域、常见问题；并附 OpenWrt 官方 Wiki 的 Tailscale 页面（https://openwrt.org/docs/guide-user/services/vpn/tailscale/start ，写文档时再核对链接）。界面上的“查看说明”链接指向这一节。

## 4. LuCI 应用（luci-app-rsagent）

### 4.1 功能

- **状态**：服务运行中/已停止、版本、tailscale 状态与 IP、各桥接状态与流量。
- **操作**：启动/停止/重启服务、开机启动开关、单个桥接启用/停用。
- **设置**：控制台端口（修改流程见 3.4）、是否允许 LAN 访问控制台、网络后端（system / embedded）。
- **Tailscale 引导**：显示 3.5 的检测结果，提供同样的安装 / 启动 / 登录按钮，未就绪时置顶显示。
- **打开控制台**：一次性令牌跳转到完整控制台。

### 4.2 组成

| 文件 | 作用 |
|---|---|
| `htdocs/luci-static/resources/view/rsagent/overview.js` | LuCI 客户端 JS 页面 |
| `root/usr/share/luci/menu.d/luci-app-rsagent.json` | 菜单：服务 → RemoteSerial |
| `root/usr/share/rpcd/acl.d/luci-app-rsagent.json` | ACL：读状态 / 执行动作分开授权 |
| `root/usr/libexec/rpcd/luci.rsagent` | rpcd 插件（shell），转调 `rsagent ctl … --json` |
| `po/zh_Hant/rsagent.po`、`po/templates/rsagent.pot` | 正體中文翻译；英文为源文字 |

### 4.3 新增命令 `rsagent ctl`

供 rpcd 插件使用，输出 JSON，内部调用本机 `127.0.0.1` API：

```
rsagent ctl status            # 服务、tailscale、桥接摘要
rsagent ctl bridge-enable <id>
rsagent ctl bridge-disable <id>
rsagent ctl settings-get
rsagent ctl settings-set '<json>'
rsagent ctl token             # 一次性控制台令牌
rsagent ctl console-port <端口> # 修改控制台端口（含检查与先开后关）
rsagent ctl tailscale-check   # 3.5 的检测结果
rsagent ctl tailscale-install # 安装并启动系统 tailscale，逐行输出进度
```

服务控制（启停、开机启动）由 rpcd 插件直接调用 `/etc/init.d/rsagent`，因为 rsagent 停止时 API 不可用。

## 5. 套件

### 5.1 内容

**rsagent**（按架构：`x86_64`、`aarch64_generic`；RK3568 的 `rockchip/armv8` 目标即为 `aarch64_generic`）

| 路径 | 说明 |
|---|---|
| `/usr/bin/rsagent` | 预编译程序（来自 `make linux-amd64/arm64`） |
| `/etc/init.d/rsagent` | procd 脚本 |
| `/etc/rsagent/`（conffile） | 配置目录 |
| `/lib/upgrade/keep.d/rsagent` | 升级固件时保留 `/etc/rsagent` |
| `/etc/uci-defaults/90-rsagent` | 首次安装：把 `tailscale0` 加入防火墙区域并放行桥接与控制台端口 |

依赖：`tailscale`、`kmod-usb-serial`、`kmod-usb-acm`；建议（apk 用 `recommends`，ipk 写在说明）：`kmod-usb-serial-ch341`、`-cp210x`、`-ftdi`、`-pl2303`。

**luci-app-rsagent**（架构无关 `all` / `noarch`），依赖 `rsagent`、`luci-base`。

### 5.2 构建

> **实际做法（v0.3）**：程序是预编译的，不需要 SDK 的交叉编译，因此改用 `tools/owpkg`：`.ipk` 由工具直接生成（与 ipkg-build 相同的 tar.gz 结构），`.apk` 用 Alpine 3.23 容器里的 `apk mkpkg` 生成（与 OpenWrt 25.x 同为 apk-tools v3）。`make openwrt-pkg VERSION=…`，产物与其他发布文件一起放在 `dist/`。下面是原计划，保留作参考。
>
> 依赖：`tailscale`、`kmod-usb-serial`。各 USB 转串口驱动（`kmod-usb-serial-ch341/-cp210x/-ftdi/-pl2303`、`kmod-usb-acm`）不作为依赖，由 `install.sh` 逐个尽量安装：厂商固件的源常缺其中某个（如 Photonicat 一代没有 `kmod-usb-acm`），作为依赖会让整个套件装不上。升级：rsagent 的套件不在官方源中，`opkg/apk upgrade` 找不到新版。改为程序内一键更新：按清单 `files` 找到本机架构的新套件，下载校验后由独立会话中的脚本交给 opkg / apk 安装（安装后脚本会重启 rsagent，所以不能在 rsagent 进程内执行）；LuCI 页面与部署页都可触发。也可再次执行 `install.sh`。

- 仓库新增 `openwrt/rsagent/Makefile` 与 `openwrt/luci-app-rsagent/Makefile`：使用预编译程序，不在 SDK 里编译 Go。
- 用官方 SDK Docker 镜像在本机（macOS 上需 Docker）构建：
  - `openwrt/sdk:x86-64-24.10.x`、`openwrt/sdk:rockchip-armv8-24.10.x` → `.ipk`
  - `openwrt/sdk:x86-64-25.x`、`openwrt/sdk:rockchip-armv8-25.x` → `.apk`
- 新增 `make openwrt VERSION=…`，产物放 `dist/openwrt/{24.10,25}/`，与其他文件一起上传到 GitHub Release。
- **签名**：`.ipk` 可直接 `opkg install`。`.apk` 未用 OpenWrt 信任的密钥签名，需 `apk add --allow-untrusted`；后续可发布自有公钥，让用户放进 `/etc/apk/keys/`。

### 5.3 升级

- 用户：`opkg install rsagent_<版本>_<arch>.ipk` 或 `apk add --allow-untrusted rsagent-<版本>.apk`。
- 程序内“检查更新”仍可用，但只提示新版本与对应的升级命令，不自行替换文件。

## 6. 分阶段实施

| 阶段 | 内容 | 完成标准 |
|---|---|---|
| **1. 核心可在 OpenWrt 运行** | OpenWrt 识别与路径；系统 Tailscale 后端；Tailscale 检测与一键安装/启动/登录引导（控制台侧）；procd 脚本；部署页适配；`install.sh` + tar 包 | 在 RK3568 与 x86_64 上：tailnet 内可访问控制台、桥接收发正常、重启路由器后自动恢复；在未装 tailscale 的干净系统上按引导能完成安装并登录 |
| **2. LuCI 与登录打通** | `rsagent ctl`；一次性令牌；LAN 访问开关；控制台端口修改（含防火墙同步）；LuCI 侧的 Tailscale 引导；luci-app-rsagent（含正體中文） | LuCI 中可启停服务、开关桥接、改控制台端口后仍能一键进入控制台且无需再登录 |
| **3. 套件与发布** | 套件 Makefile；SDK 构建脚本；`make openwrt`；README 与使用说明更新 | 24.10 与 25.x 各在 x86_64、aarch64 上安装、升级、卸载通过，配置在升级固件后保留 |

## 7. 测试

- **x86_64**：用 OpenWrt 24.10 与 25.x 的官方 x86-64 镜像跑虚拟机（QEMU / UTM），USB 串口直通或用 `socat` 虚拟串口。
- **RK3568**：实机测试，覆盖 CH340、CP210x、FTDI、CDC-ACM 四类常见 USB 串口与热插拔。
- 重点场景：未安装 tailscale 的干净系统、无外网时的安装失败与切换 embedded、修改控制台端口（含冲突端口）、tailscaled 晚于 rsagent 启动、tailscale IP 变化、路由器重启、固件升级后配置保留、LuCI 与控制台同时操作。

## 8. 风险与待定问题

1. **自带 tailscale 版本**：需实测两个 OpenWrt 版本里 tailscale 的版本，确认 LocalAPI 接口兼容性。
2. **与 luci-app-tailscale 共存**：两边都能改 tailscale 设置。rsagent 默认只读，只在用户明确操作（如加子网路由）时写入，并且只动自己加的项目。
3. **内存**：系统模式不跑 tsnet，预计常驻内存明显下降，需实测；必要时设置 `GOMEMLIMIT`。
4. **apk 签名**：是否发布自有签名密钥（影响用户安装体验）。
5. **是否需要 embedded 模式**：OpenWrt 上是否保留“内嵌 Tailscale”作为备选（例如路由器不想装 tailscale 套件的情况）。计划中保留，默认 system。

## 附录 A：当前程序在各架构的体积

`-trimpath -ldflags "-s -w"`，2026-11-09 实测（含内嵌 tsnet 与前端）：

| 架构 | 程序 | gzip 后 |
|---|---|---|
| x86_64 | 23 MB | 9.0 MB |
| aarch64 | 22 MB | 8.2 MB |
| armv7 | 22 MB | 8.5 MB |
| mipsle / mips（softfloat） | 25 MB | 8.2–8.3 MB |
