// Package deploy 提供部署页的检测与动作：开机自启计划任务、系统服务、防火墙规则、
// WebView2 运行时、自动登录状态，以及需要提权时拉起提权子进程。
//
// 动作由 `rsagent deploy <action>` 子进程执行，结果写到数据目录下的 deploy-result.json，
// 这样提权后的子进程也能把错误信息带回给界面。
package deploy

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"rsagent/internal/i18n"
	"runtime"
	"strings"
	"time"

	"rsagent/internal/platform"
	"rsagent/internal/update"
)

// Options 是执行部署动作需要的环境。
type Options struct {
	ExePath string
	DataDir string
	Listen  string
	UDPPort int
	Log     *slog.Logger
}

// Status 是部署页的检测结果。
type Status struct {
	OS       string         `json:"os"`
	Platform string         `json:"platform"` // os，或 openwrt
	Elevated bool           `json:"elevated"`
	ExePath  string         `json:"exePath"`
	DataDir  string         `json:"dataDir"`
	Task     TaskStatus     `json:"task"`
	Service  ServiceStatus  `json:"service"`
	Firewall FirewallStatus `json:"firewall"`
	WebView2 WebView2Status `json:"webview2"`
	AutoLogo AutoLogon      `json:"autoLogon"`
	Login    LoginItem      `json:"loginItem"`
}

// LoginItem 是 macOS 的“登录后启动”：用户级 LaunchAgent，登录时在后台打开 .app。
type LoginItem struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Bundle    string `json:"bundle,omitempty"` // 登录时打开的 .app
	Detail    string `json:"detail,omitempty"`
}

// TaskStatus 是开机自启计划任务的状态。
type TaskStatus struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	RunLevel  string `json:"runLevel,omitempty"` // Highest | Limited
	Command   string `json:"command,omitempty"`
}

// ServiceStatus 是系统服务的状态。
type ServiceStatus struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Enabled   bool   `json:"enabled"`            // OpenWrt：开机启动
	Manager   string `json:"manager,omitempty"`  // procd（OpenWrt）
	Packaged  string `json:"packaged,omitempty"` // OpenWrt：由 opkg / apk 安装
	Binary    string `json:"binary,omitempty"`   // 服务使用的程序副本
	Detail    string `json:"detail,omitempty"`
}

// FirewallStatus 是防火墙入站规则的状态。
type FirewallStatus struct {
	Supported bool `json:"supported"`
	Installed bool `json:"installed"`
	UDPPort   int  `json:"udpPort"`
}

// WebView2Status 是 WebView2 运行时的状态。
type WebView2Status struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
}

// AutoLogon 是 Windows 自动登录配置的状态，只读。
type AutoLogon struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	User      string `json:"user,omitempty"`
}

// Result 是子进程写回的动作结果。
type Result struct {
	Action  string    `json:"action"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// 动作名。
const (
	ActionTaskInstall     = "task-install"
	ActionTaskRemove      = "task-remove"
	ActionServiceInstall  = "service-install"
	ActionServiceRemove   = "service-remove"
	ActionServiceStart    = "service-start"
	ActionServiceStop     = "service-stop"
	ActionServiceRestart  = "service-restart"
	ActionFirewallAdd     = "firewall-add"
	ActionFirewallRemove  = "firewall-remove"
	ActionWebView2Install = "webview2-install"
	ActionUpdateApply     = "update-apply" // 用已下载的新程序替换可执行文件
	ActionLoginEnable     = "login-enable" // macOS：登录后启动 .app
	ActionLoginDisable    = "login-disable"
)

// Actions 是全部合法动作。
var Actions = []string{
	ActionTaskInstall, ActionTaskRemove,
	ActionServiceInstall, ActionServiceRemove, ActionServiceStart, ActionServiceStop, ActionServiceRestart,
	ActionFirewallAdd, ActionFirewallRemove,
	ActionWebView2Install,
	ActionUpdateApply,
	ActionLoginEnable, ActionLoginDisable,
}

// ValidAction 判断动作名是否合法。
func ValidAction(a string) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// NeedsElevation 判断动作在当前平台是否需要管理员权限。
func NeedsElevation(o Options, action string) bool {
	switch action {
	case ActionTaskInstall, ActionTaskRemove:
		// 当前用户的登录触发任务不需要提权；提权后会以最高权限注册。
		return false
	case ActionWebView2Install:
		// 引导程序自己会弹 UAC。
		return false
	case ActionLoginEnable, ActionLoginDisable:
		// 用户自己的 LaunchAgent
		return false
	case ActionUpdateApply:
		// 目标目录当前用户能写就不用提权；要替换正在运行的服务副本时需要提权重启服务。
		_, _, binSum, ok := update.PendingBinary(o.DataDir)
		for _, t := range updateTargets(o) {
			if ok {
				if sum, err := update.FileSHA256(t.exe); err == nil && strings.EqualFold(sum, binSum) {
					continue // 已是新版本
				}
			}
			if !writableDir(t.dir()) {
				return true
			}
			if sb := ServiceBinary(o); t.exe == sb && !sameFile(sb, o.ExePath) {
				if st := serviceStatus(o); st.Installed && st.Running {
					return true
				}
			}
		}
		return false
	default:
		return true
	}
}

// GetStatus 返回当前平台的检测结果。
func GetStatus(o Options) Status {
	st := Status{OS: runtime.GOOS, Platform: platform.Name(), ExePath: o.ExePath, DataDir: o.DataDir}
	fillStatus(&st, o)
	return st
}

// Run 直接执行动作，要求调用方已具备所需权限。
func Run(o Options, action string) error {
	if !ValidAction(action) {
		return i18n.Errorf("未知动作 %q", action)
	}
	return runAction(o, action)
}

// Execute 在当前进程或提权子进程中执行动作，返回给界面的结果。
func Execute(o Options, action string) Result {
	res := Result{Action: action, Time: time.Now()}
	if !ValidAction(action) {
		res.Message = i18n.T("未知动作")
		return res
	}
	if NeedsElevation(o, action) && !isElevated() {
		if err := elevateAndWait(o, action); err != nil {
			res.Message = err.Error()
			if r, rerr := readResult(o.DataDir); rerr == nil && r.Action == action && !r.OK && r.Message != "" {
				res.Message = r.Message
			}
			return res
		}
		if r, err := readResult(o.DataDir); err == nil && r.Action == action {
			return r
		}
		res.OK = true
		res.Message = i18n.T("完成")
		return res
	}
	if err := runAction(o, action); err != nil {
		res.Message = err.Error()
		return res
	}
	res.OK = true
	res.Message = i18n.T("完成")
	return res
}

// ExecuteDetached 在独立子进程里执行动作且不等待结果，用于服务停止或卸载自己：
// 发起请求的进程会被动作本身结束，所以不能同步等待。需要提权时弹 UAC。
func ExecuteDetached(o Options, action string) error {
	if !ValidAction(action) {
		return i18n.Errorf("未知动作 %q", action)
	}
	return spawnDetached(o, action, NeedsElevation(o, action) && !isElevated())
}

// updateTarget 是一个要更新的程序；bundle 非空时（macOS 的 .app）替换整个包。
type updateTarget struct {
	exe    string
	bundle string
}

func (t updateTarget) dir() string {
	if t.bundle != "" {
		return filepath.Dir(t.bundle)
	}
	return filepath.Dir(t.exe)
}

// updateTargets 列出更新时要替换的程序：本程序，以及已安装服务使用的副本。
func updateTargets(o Options) []updateTarget {
	targets := []updateTarget{{exe: o.ExePath, bundle: update.BundleOf(o.ExePath)}}
	if st := serviceStatus(o); st.Installed {
		if sb := ServiceBinary(o); !sameFile(sb, o.ExePath) {
			targets = append(targets, updateTarget{exe: sb})
		}
	}
	return targets
}

// updateApply 用 update 目录里已下载并校验过的程序替换目标文件。
//
// 已经是新版本（SHA-256 相同）的目标跳过。服务的重启由调用方决定：服务进程自己
// 调用时不能在这里停自己。
func updateApply(o Options) (replaced []string, err error) {
	p, bin, binSum, ok := update.PendingBinary(o.DataDir)
	if !ok {
		return nil, i18n.New("没有已下载的更新，请先下载")
	}
	for _, t := range updateTargets(o) {
		if sum, err := update.FileSHA256(t.exe); err == nil && strings.EqualFold(sum, binSum) {
			continue
		}
		if t.bundle != "" {
			if err := update.ReplaceBundle(t.bundle, p.File); err != nil {
				return replaced, i18n.Errorf("替换 %s: %w", t.bundle, err)
			}
		} else {
			if err := update.ReplaceExecutable(t.exe, bin); err != nil {
				return replaced, i18n.Errorf("替换 %s: %w", t.exe, err)
			}
			fixOwner(o.DataDir, t.exe)
		}
		replaced = append(replaced, t.exe)
	}
	if o.Log != nil {
		o.Log.Info(i18n.T("已替换程序文件"), "version", p.Version, "files", replaced)
	}
	return replaced, nil
}

// writableDir 判断当前进程能否在目录里创建文件。
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".rsagent-w-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// WriteResult 由子进程调用，把结果写回数据目录。
func WriteResult(dataDir string, r Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(resultPath(dataDir), data, 0o644); err != nil {
		return err
	}
	fixOwner(dataDir, resultPath(dataDir))
	return nil
}

func readResult(dataDir string) (Result, error) {
	var r Result
	data, err := os.ReadFile(resultPath(dataDir))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if time.Since(r.Time) > 10*time.Minute {
		return r, i18n.New("结果已过期")
	}
	return r, nil
}

func resultPath(dataDir string) string { return filepath.Join(dataDir, "deploy-result.json") }

// quoteArg 给含空格的路径加引号，用于命令行拼接。
func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
