// Package systs 检测与管理系统自带的 tailscale（OpenWrt 的 tailscale 套件）：
// 是否已安装、服务是否启用与运行、防火墙区域，以及一键安装、启动和配置防火墙。
//
// 安装与启动只在 OpenWrt 上以 root 运行时提供；其他平台只做检测，界面给出手动命令。
package systs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rsagent/internal/i18n"
	"rsagent/internal/platform"
)

// Info 是系统 tailscale 的检测结果。
type Info struct {
	Installed      bool   `json:"installed"`
	Binary         string `json:"binary,omitempty"`
	ServiceScript  bool   `json:"serviceScript"`  // /etc/init.d/tailscale 存在
	ServiceEnabled bool   `json:"serviceEnabled"` // 开机启动
	DaemonRunning  bool   `json:"daemonRunning"`  // 有 tailscaled 进程
	Socket         string `json:"socket"`
	SocketOK       bool   `json:"socketOK"`               // LocalAPI 可连接，由 tsnode 填写
	Version        string `json:"version,omitempty"`      // tailscaled 版本，连接后由 tsnode 填写
	FirewallZone   *bool  `json:"firewallZone,omitempty"` // OpenWrt：tailscale0 已加入防火墙区域
	PkgManager     string `json:"pkgManager,omitempty"`   // opkg | apk
	InstallCommand string `json:"installCommand,omitempty"`
	CanManage      bool   `json:"canManage"`        // 本程序可直接安装、启动（OpenWrt 且为 root）
	FreeMB         int    `json:"freeMB,omitempty"` // 安装位置的剩余空间
	NeedMB         int    `json:"needMB,omitempty"` // 安装所需的大致空间
}

const (
	initScript = "/etc/init.d/tailscale"
	// 套件约 10MB 压缩、展开后二十多 MB，按 35MB 预留，留出套件索引与余量。
	needMB = 35
)

// Runner 执行命令并把输出写到 out；测试时替换。
var Runner = func(ctx context.Context, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

// output 执行命令并返回输出。
func output(ctx context.Context, name string, args ...string) (string, error) {
	var b strings.Builder
	err := Runner(ctx, &b, name, args...)
	return b.String(), err
}

// Detect 检测系统 tailscale。socket 是 LocalAPI 路径，只用于填写结果。
func Detect(socket string) Info {
	info := Info{Socket: socket}
	for _, p := range []string{"/usr/sbin/tailscaled", "/usr/bin/tailscaled"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			info.Binary = p
			break
		}
	}
	if info.Binary == "" {
		if p, err := exec.LookPath("tailscaled"); err == nil {
			info.Binary = p
		}
	}
	info.Installed = info.Binary != ""
	info.DaemonRunning = processRunning("tailscaled")
	if !platform.OpenWrt() {
		return info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := os.Stat(initScript); err == nil {
		info.ServiceScript = true
		info.ServiceEnabled = Runner(ctx, io.Discard, initScript, "enabled") == nil
	}
	zone := HasFirewallZone(ctx)
	info.FirewallZone = &zone
	info.PkgManager = platform.PkgManager()
	info.InstallCommand = platform.InstallCommand(info.PkgManager, "tailscale")
	info.CanManage = os.Geteuid() == 0 && info.PkgManager != ""
	if !info.Installed {
		info.NeedMB = needMB
		info.FreeMB = freeMB(installRoot())
	}
	return info
}

// installRoot 是套件安装所在的可写分区：有 overlay 时为 /overlay。
func installRoot() string {
	if fi, err := os.Stat("/overlay"); err == nil && fi.IsDir() {
		return "/overlay"
	}
	return "/"
}

// processRunning 在 /proc 中查找进程名；没有 /proc 的系统返回 false。
func processRunning(name string) bool {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(b)) == name {
			return true
		}
	}
	return false
}

func requireManage() error {
	if !platform.OpenWrt() {
		return i18n.New("只有 OpenWrt 上才能由 rsagent 安装系统 Tailscale，请用系统的套件管理器安装")
	}
	if os.Geteuid() != 0 {
		return i18n.New("需要以 root 运行 rsagent 才能安装系统 Tailscale")
	}
	return nil
}

// Install 用套件管理器安装 tailscale，再启动服务并配置防火墙。进度写到 out。
func Install(ctx context.Context, out io.Writer) error {
	if err := requireManage(); err != nil {
		return err
	}
	mgr := platform.PkgManager()
	if mgr == "" {
		return i18n.New("找不到套件管理器（opkg 或 apk）")
	}
	if free := freeMB(installRoot()); free > 0 && free < needMB {
		return i18n.Errorf("剩余空间不足：%s 只剩 %d MB，安装 tailscale 约需 %d MB", installRoot(), free, needMB)
	}
	step(out, i18n.T("更新套件列表"))
	var err error
	if mgr == "apk" {
		err = Runner(ctx, out, "apk", "update")
	} else {
		err = Runner(ctx, out, "opkg", "update")
	}
	if err != nil {
		return i18n.Errorf("更新套件列表失败（请检查路由器能否连网）: %w", err)
	}
	step(out, i18n.T("安装 tailscale"))
	if mgr == "apk" {
		err = Runner(ctx, out, "apk", "add", "tailscale")
	} else {
		err = Runner(ctx, out, "opkg", "install", "tailscale")
	}
	if err != nil {
		return i18n.Errorf("安装 tailscale 失败: %w", err)
	}
	if err := StartService(ctx, out); err != nil {
		return err
	}
	return EnsureFirewall(ctx, out)
}

// StartService 启用并启动系统 tailscale 服务。
func StartService(ctx context.Context, out io.Writer) error {
	if err := requireManage(); err != nil {
		return err
	}
	if _, err := os.Stat(initScript); err != nil {
		return i18n.Errorf("找不到 %s，tailscale 套件可能未安装完整", initScript)
	}
	step(out, i18n.T("设为开机启动并启动 tailscale"))
	if err := Runner(ctx, out, initScript, "enable"); err != nil {
		return i18n.Errorf("设置开机启动失败: %w", err)
	}
	if err := Runner(ctx, out, initScript, "start"); err != nil {
		return i18n.Errorf("启动 tailscale 失败: %w", err)
	}
	return nil
}

// HasFirewallZone 判断 tailscale0 是否已在某个防火墙区域中。
func HasFirewallZone(ctx context.Context) bool {
	s, err := output(ctx, "uci", "-q", "show", "firewall")
	if err != nil {
		return false
	}
	return strings.Contains(s, "'tailscale0'")
}

// 防火墙配置的 uci 节名，便于识别与卸载。
const (
	fwZone = "rsagent_ts"
	fwFwd  = "rsagent_ts_lan"
)

// FirewallCommands 返回把 tailscale0 加入新区域 "tailscale" 的 uci 命令：允许访问路由器本身
// （桥接与管理界面端口），并允许转发到 lan（子网路由）。hasLan 为 false 时不加转发。
func FirewallCommands(hasLan bool) [][]string {
	z := "firewall." + fwZone
	cmds := [][]string{
		{"uci", "set", z + "=zone"},
		{"uci", "set", z + ".name=tailscale"},
		{"uci", "add_list", z + ".device=tailscale0"},
		{"uci", "set", z + ".input=ACCEPT"},
		{"uci", "set", z + ".output=ACCEPT"},
		{"uci", "set", z + ".forward=ACCEPT"},
	}
	if hasLan {
		f := "firewall." + fwFwd
		cmds = append(cmds,
			[]string{"uci", "set", f + "=forwarding"},
			[]string{"uci", "set", f + ".src=tailscale"},
			[]string{"uci", "set", f + ".dest=lan"},
		)
	}
	return append(cmds, []string{"uci", "commit", "firewall"}, []string{"/etc/init.d/firewall", "reload"})
}

// EnsureFirewall 在 tailscale0 还不属于任何区域时新建区域。已有则不改动。
func EnsureFirewall(ctx context.Context, out io.Writer) error {
	if err := requireManage(); err != nil {
		return err
	}
	if HasFirewallZone(ctx) {
		step(out, i18n.T("防火墙中已有 tailscale0 的区域，跳过"))
		return nil
	}
	step(out, i18n.T("把 tailscale0 加入防火墙区域"))
	all, _ := output(ctx, "uci", "-q", "show", "firewall")
	hasLan := strings.Contains(all, ".name='lan'")
	for _, c := range FirewallCommands(hasLan) {
		if err := Runner(ctx, out, c[0], c[1:]...); err != nil {
			return i18n.Errorf("配置防火墙失败（%s）: %w", strings.Join(c, " "), err)
		}
	}
	return nil
}

func step(out io.Writer, msg string) {
	fmt.Fprintf(out, "==> %s\n", msg)
}

// consoleRule 是放行局域网访问 rsagent 管理界面的防火墙规则（uci 段名）。
const consoleRule = "rsagent_console"

// ConsoleFirewallCommands 返回同步管理界面防火墙规则的命令：允许局域网访问时放行 lan 区域到该端口，
// 否则删除规则。多数 OpenWrt 的 lan 区域本来就放行入站，规则主要是给收紧了 lan 区域的路由器用。
func ConsoleFirewallCommands(port int, lan bool) [][]string {
	r := "firewall." + consoleRule
	if !lan {
		return [][]string{{"uci", "-q", "delete", r}, {"uci", "commit", "firewall"}, {"/etc/init.d/firewall", "reload"}}
	}
	return [][]string{
		{"uci", "set", r + "=rule"},
		{"uci", "set", r + ".name=Allow-rsagent-console"},
		{"uci", "set", r + ".src=lan"},
		{"uci", "set", r + ".proto=tcp"},
		{"uci", "set", r + ".dest_port=" + strconv.Itoa(port)},
		{"uci", "set", r + ".target=ACCEPT"},
		{"uci", "commit", "firewall"},
		{"/etc/init.d/firewall", "reload"},
	}
}

// SyncConsoleFirewall 按当前设置写入或删除管理界面的防火墙规则，规则已经一致时不动防火墙。
// 不是 OpenWrt 或不是 root 时什么都不做。
func SyncConsoleFirewall(ctx context.Context, port int, lan bool) error {
	if !platform.OpenWrt() || os.Geteuid() != 0 {
		return nil
	}
	cur, _ := output(ctx, "uci", "-q", "get", "firewall."+consoleRule+".dest_port")
	cur = strings.TrimSpace(cur)
	if (lan && cur == strconv.Itoa(port)) || (!lan && cur == "") {
		return nil
	}
	for _, c := range ConsoleFirewallCommands(port, lan) {
		// 删除不存在的规则会失败，忽略
		if err := Runner(ctx, io.Discard, c[0], c[1:]...); err != nil && c[1] != "-q" {
			return i18n.Errorf("配置防火墙失败（%s）: %w", strings.Join(c, " "), err)
		}
	}
	return nil
}
