// Package deploy 提供部署页的检测与动作：开机自启计划任务、系统服务、防火墙规则、
// WebView2 运行时、自动登录状态，以及需要提权时拉起提权子进程。
//
// 动作由 `rsagent deploy <action>` 子进程执行，结果写到数据目录下的 deploy-result.json，
// 这样提权后的子进程也能把错误信息带回给界面。
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	Elevated bool           `json:"elevated"`
	ExePath  string         `json:"exePath"`
	DataDir  string         `json:"dataDir"`
	Task     TaskStatus     `json:"task"`
	Service  ServiceStatus  `json:"service"`
	Firewall FirewallStatus `json:"firewall"`
	WebView2 WebView2Status `json:"webview2"`
	AutoLogo AutoLogon      `json:"autoLogon"`
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
	Binary    string `json:"binary,omitempty"` // 服务使用的程序副本
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
	ActionFirewallAdd     = "firewall-add"
	ActionFirewallRemove  = "firewall-remove"
	ActionWebView2Install = "webview2-install"
)

// Actions 是全部合法动作。
var Actions = []string{
	ActionTaskInstall, ActionTaskRemove,
	ActionServiceInstall, ActionServiceRemove, ActionServiceStart, ActionServiceStop,
	ActionFirewallAdd, ActionFirewallRemove,
	ActionWebView2Install,
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
func NeedsElevation(action string) bool {
	switch action {
	case ActionTaskInstall, ActionTaskRemove:
		// 当前用户的登录触发任务不需要提权；提权后会以最高权限注册。
		return false
	case ActionWebView2Install:
		// 引导程序自己会弹 UAC。
		return false
	default:
		return true
	}
}

// GetStatus 返回当前平台的检测结果。
func GetStatus(o Options) Status {
	st := Status{OS: runtime.GOOS, ExePath: o.ExePath, DataDir: o.DataDir}
	fillStatus(&st, o)
	return st
}

// Run 直接执行动作，要求调用方已具备所需权限。
func Run(o Options, action string) error {
	if !ValidAction(action) {
		return fmt.Errorf("未知动作 %q", action)
	}
	return runAction(o, action)
}

// Execute 在当前进程或提权子进程中执行动作，返回给界面的结果。
func Execute(o Options, action string) Result {
	res := Result{Action: action, Time: time.Now()}
	if !ValidAction(action) {
		res.Message = "未知动作"
		return res
	}
	if NeedsElevation(action) && !isElevated() {
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
		res.Message = "完成"
		return res
	}
	if err := runAction(o, action); err != nil {
		res.Message = err.Error()
		return res
	}
	res.OK = true
	res.Message = "完成"
	return res
}

// ExecuteDetached 在独立子进程里执行动作且不等待结果，用于服务停止或卸载自己：
// 发起请求的进程会被动作本身结束，所以不能同步等待。需要提权时弹 UAC。
func ExecuteDetached(o Options, action string) error {
	if !ValidAction(action) {
		return fmt.Errorf("未知动作 %q", action)
	}
	return spawnDetached(o, action, NeedsElevation(action) && !isElevated())
}

// WriteResult 由子进程调用，把结果写回数据目录。
func WriteResult(dataDir string, r Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(resultPath(dataDir), data, 0o644)
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
		return r, errors.New("结果已过期")
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
