//go:build darwin

package deploy

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kardianos/service"

	"rsagent/internal/i18n"
)

// launchd 系统守护进程的 plist。安装与启停需要 root，查询状态不需要。
const darwinPlist = "/Library/LaunchDaemons/" + ServiceName + ".plist"

func isElevated() bool { return os.Geteuid() == 0 }

func fillStatus(st *Status, o Options) {
	st.Elevated = isElevated()
	st.Task = TaskStatus{Supported: false}
	st.Service = serviceStatus(o)
	st.Firewall = FirewallStatus{Supported: false, UDPPort: o.UDPPort}
	st.WebView2 = WebView2Status{Supported: false}
	st.AutoLogo = AutoLogon{Supported: false}
	st.Login = loginStatus(o)
}

func runAction(o Options, action string) error {
	switch action {
	case ActionServiceInstall, ActionServiceRemove, ActionServiceStart, ActionServiceStop, ActionServiceRestart:
		return serviceAction(o, action)
	case ActionUpdateApply:
		return updateApplyAndRestartService(o)
	case ActionLoginEnable:
		return loginEnable(o)
	case ActionLoginDisable:
		return loginDisable()
	case ActionTaskInstall, ActionTaskRemove, ActionFirewallAdd, ActionFirewallRemove, ActionWebView2Install:
		return i18n.Errorf("动作 %q 仅支持 Windows", action)
	}
	return i18n.Errorf("未知动作 %q", action)
}

// platformServiceConfig 让 launchd 以数据目录属主（通常就是当前用户）的身份运行 core：
// 开机即启动、不依赖登录，但读写的都是用户自己的数据目录，也不需要 root 权限。
func platformServiceConfig(o Options, c *service.Config) {
	c.Option["RunAtLoad"] = true
	c.Option["KeepAlive"] = true
	// 默认写到 /var/log，以普通用户运行时没有权限
	c.Option["LogDirectory"] = filepath.Join(o.DataDir, "logs")
	uid, _, ok := dataDirOwner(o.DataDir)
	if !ok || uid == 0 {
		return
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return
	}
	c.UserName = u.Username
	c.EnvVars = map[string]string{"HOME": u.HomeDir, "USER": u.Username}
}

func launchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var runningRe = regexp.MustCompile(`(?m)^\s*state = running\s*$`)

// queryServiceStatus 不依赖 kardianos：它用 launchctl list，普通用户看不到系统域的守护进程。
func queryServiceStatus(Options) (installed, running bool, err error) {
	if _, err := os.Stat(darwinPlist); err != nil {
		return false, false, nil
	}
	out, err := launchctl("print", "system/"+ServiceName)
	if err != nil {
		return true, false, nil // 已安装但未加载
	}
	return true, runningRe.MatchString(out), nil
}

func svcStart(Options, service.Service) error {
	if _, err := launchctl("print", "system/"+ServiceName); err == nil {
		out, err := launchctl("kickstart", "system/"+ServiceName)
		if err != nil {
			return i18n.Errorf("launchctl kickstart 失败: %s", out)
		}
		return nil
	}
	if out, err := launchctl("bootstrap", "system", darwinPlist); err != nil {
		return i18n.Errorf("launchctl bootstrap 失败: %s", out)
	}
	return nil
}

func svcStop(Options, service.Service) error {
	if _, err := launchctl("print", "system/"+ServiceName); err != nil {
		return nil // 没有加载
	}
	if out, err := launchctl("bootout", "system/"+ServiceName); err != nil {
		return i18n.Errorf("launchctl bootout 失败: %s", out)
	}
	return nil
}

// updateServiceBinPath 重写 plist（程序路径、运行身份可能变了），由调用方随后启动。
func updateServiceBinPath(o Options) error {
	_ = svcStop(o, nil)
	_ = os.Remove(darwinPlist)
	svc, err := newControlService(o)
	if err != nil {
		return err
	}
	return svc.Install()
}

// shellQuote 为 /bin/sh 加单引号。
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// appleScriptString 转义为 AppleScript 字符串字面量。
func appleScriptString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func deployCommand(o Options, action string) string {
	args := []string{o.ExePath, "deploy", action, "--data-dir", o.DataDir}
	if o.Listen != "" {
		args = append(args, "--listen", o.Listen)
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// adminScript 生成弹出系统管理员授权框并以 root 执行命令的 AppleScript。
func adminScript(cmd string) string {
	return "do shell script " + appleScriptString(cmd) +
		" with prompt " + appleScriptString(i18n.T("RemoteSerial Agent 需要管理员权限来管理系统服务或更新程序。")) +
		" with administrator privileges"
}

func adminError(out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "(-128)") {
		return i18n.New("用户取消了管理员授权")
	}
	if msg == "" {
		msg = err.Error()
	}
	return i18n.Errorf("管理员授权执行失败: %s", msg)
}

// elevateAndWait 通过系统授权框以 root 运行 `rsagent deploy <action>` 并等待。
// 必须在用户的图形会话里调用（托盘/窗口进程，或在其中运行的 core）；
// 以服务方式运行的 core 是后台守护进程，弹不出授权框。
func elevateAndWait(o Options, action string) error {
	out, err := exec.Command("osascript", "-e", adminScript(deployCommand(o, action))).CombinedOutput()
	if err != nil {
		return adminError(out, err)
	}
	return nil
}

func spawnDetached(o Options, action string, elevate bool) error {
	if elevate {
		// 授权后命令在后台执行，osascript 立即返回；动作可能结束本进程，所以不等待。
		cmd := exec.Command("osascript", "-e", adminScript(deployCommand(o, action)+" >/dev/null 2>&1 &"))
		return cmd.Start()
	}
	args := []string{"deploy", action, "--data-dir", o.DataDir}
	if o.Listen != "" {
		args = append(args, "--listen", o.Listen)
	}
	return exec.Command(o.ExePath, args...).Start()
}
