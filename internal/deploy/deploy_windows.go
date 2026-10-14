//go:build windows

package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	taskName     = "RemoteSerial Agent"
	firewallRule = "RemoteSerial Agent (UDP)"
	webview2URL  = "https://go.microsoft.com/fwlink/p/?LinkId=2124703" // Evergreen 引导程序
)

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func fillStatus(st *Status, o Options) {
	st.Elevated = isElevated()
	st.Task = taskStatus()
	st.Service = serviceStatus(o)
	st.Firewall = FirewallStatus{Supported: true, Installed: firewallRuleExists(), UDPPort: o.UDPPort}
	st.WebView2 = webview2Status()
	st.AutoLogo = autoLogonStatus()
}

func runAction(o Options, action string) error {
	switch action {
	case ActionTaskInstall:
		return taskInstall(o)
	case ActionTaskRemove:
		return taskRemove()
	case ActionServiceInstall, ActionServiceRemove, ActionServiceStart, ActionServiceStop:
		return serviceAction(o, action)
	case ActionFirewallAdd:
		return firewallAdd(o)
	case ActionFirewallRemove:
		return firewallRemove()
	case ActionWebView2Install:
		return webview2Install()
	}
	return fmt.Errorf("未知动作 %q", action)
}

// ---- 计划任务 ----

func taskStatus() TaskStatus {
	st := TaskStatus{Supported: true}
	out, err := runHidden("schtasks", "/Query", "/TN", taskName, "/XML")
	if err != nil {
		return st
	}
	st.Installed = true
	xml := decodeConsole(out)
	// schtasks 导出时省略默认的 LeastPrivilege，所以没有 HighestAvailable 就是普通权限。
	st.RunLevel = "Limited"
	if strings.Contains(xml, "HighestAvailable") {
		st.RunLevel = "Highest"
	}
	if i := strings.Index(xml, "<Command>"); i >= 0 {
		rest := xml[i+len("<Command>"):]
		if j := strings.Index(rest, "</Command>"); j >= 0 {
			st.Command = rest[:j]
		}
	}
	return st
}

func taskInstall(o Options) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("获取当前用户: %w", err)
	}
	runLevel := "LeastPrivilege"
	if isElevated() {
		runLevel = "HighestAvailable"
	}
	args := "ui --data-dir " + quoteArg(o.DataDir)
	if o.Listen != "" {
		args += " --listen " + o.Listen
	}
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>RemoteSerial Agent：用户登录时启动，每 5 分钟检查一次，已运行则不重复启动</Description>
    <URI>\%s</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%s</UserId>
      <Delay>PT10S</Delay>
      <Repetition>
        <Interval>PT5M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>%s</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, xmlEscape(taskName), xmlEscape(u.Username), xmlEscape(u.Username), runLevel,
		xmlEscape(o.ExePath), xmlEscape(args), xmlEscape(filepath.Dir(o.ExePath)))

	tmp := filepath.Join(os.TempDir(), "rsagent-task.xml")
	if err := os.WriteFile(tmp, utf16LE(xml), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	out, err := runHidden("schtasks", "/Create", "/TN", taskName, "/XML", tmp, "/F")
	if err != nil {
		return fmt.Errorf("schtasks 创建失败: %s", strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

func taskRemove() error {
	out, err := runHidden("schtasks", "/Delete", "/TN", taskName, "/F")
	if err != nil {
		if !taskStatus().Installed {
			return nil
		}
		return fmt.Errorf("schtasks 删除失败: %s", strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

// ---- 服务 ----

// queryServiceStatus 只申请查询权限。kardianos 的 Status 会同时申请 START/STOP 权限，
// 普通用户进程会得到 Access is denied，把已安装的服务误报成未安装。
func queryServiceStatus(Options) (installed, running bool, err error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false, fmt.Errorf("连接服务管理器: %w", err)
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(ServiceName)
	h, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			return false, false, nil
		}
		return false, false, fmt.Errorf("打开服务: %w", err)
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return true, false, fmt.Errorf("查询服务状态: %w", err)
	}
	running = st.CurrentState == windows.SERVICE_RUNNING || st.CurrentState == windows.SERVICE_START_PENDING
	return true, running, nil
}

// updateServiceBinPath 直接改已注册服务的可执行路径与参数，避免卸载后立刻重装
// 碰到"服务已标记为删除"。
func updateServiceBinPath(o Options) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	parts := []string{quoteArg(ServiceBinary(o))}
	for _, a := range serviceArgs(o) {
		parts = append(parts, quoteArg(a))
	}
	c.BinaryPathName = strings.Join(parts, " ")
	return s.UpdateConfig(c)
}

// ---- 防火墙 ----

func firewallRuleExists() bool {
	_, err := runHidden("netsh", "advfirewall", "firewall", "show", "rule", "name="+firewallRule)
	return err == nil
}

func firewallAdd(o Options) error {
	if firewallRuleExists() {
		_ = firewallRemove()
	}
	port := o.UDPPort
	if port <= 0 {
		port = 41641
	}
	out, err := runHidden("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+firewallRule, "dir=in", "action=allow", "protocol=UDP",
		fmt.Sprintf("localport=%d", port), "program="+o.ExePath, "enable=yes", "profile=any")
	if err != nil {
		return fmt.Errorf("netsh 添加规则失败: %s", strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

func firewallRemove() error {
	out, err := runHidden("netsh", "advfirewall", "firewall", "delete", "rule", "name="+firewallRule)
	if err != nil {
		return fmt.Errorf("netsh 删除规则失败: %s", strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

// ---- WebView2 ----

func webview2Status() WebView2Status {
	st := WebView2Status{Supported: true}
	keys := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
	}
	for _, k := range keys {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := key.GetStringValue("pv")
		key.Close()
		if err == nil && v != "" && v != "0.0.0.0" {
			st.Installed = true
			st.Version = v
			return st
		}
	}
	return st
}

func webview2Install() error {
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Get(webview2URL)
	if err != nil {
		return fmt.Errorf("下载 WebView2 引导程序失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 WebView2 引导程序失败: HTTP %d", resp.StatusCode)
	}
	tmp := filepath.Join(os.TempDir(), "MicrosoftEdgeWebview2Setup.exe")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	// 引导程序自己会请求提权并显示进度，这里不等待。
	return exec.Command(tmp).Start()
}

// ---- 自动登录 ----

func autoLogonStatus() AutoLogon {
	st := AutoLogon{Supported: true}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`, registry.QUERY_VALUE)
	if err != nil {
		return st
	}
	defer key.Close()
	if v, _, err := key.GetStringValue("AutoAdminLogon"); err == nil && strings.TrimSpace(v) == "1" {
		st.Enabled = true
	}
	if v, _, err := key.GetStringValue("DefaultUserName"); err == nil {
		st.User = v
	}
	return st
}

// ---- 提权子进程 ----

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

// elevateAndWait 以管理员身份运行 `rsagent deploy <action>` 并等待退出。
func elevateAndWait(o Options, action string) error {
	const seeMaskNoCloseProcess = 0x00000040
	params := "deploy " + action + " --data-dir " + quoteArg(o.DataDir)
	if o.Listen != "" {
		params += " --listen " + o.Listen
	}
	info := shellExecuteInfo{
		fMask: seeMaskNoCloseProcess,
		nShow: int32(windows.SW_HIDE),
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	info.lpVerb, _ = windows.UTF16PtrFromString("runas")
	info.lpFile, _ = windows.UTF16PtrFromString(o.ExePath)
	info.lpParameters, _ = windows.UTF16PtrFromString(params)
	info.lpDirectory, _ = windows.UTF16PtrFromString(filepath.Dir(o.ExePath))

	r, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		var errno syscall.Errno
		if errors.As(callErr, &errno) && errno == 1223 {
			return errors.New("用户取消了 UAC 提权")
		}
		return fmt.Errorf("提权启动失败: %v", callErr)
	}
	if info.hProcess == 0 {
		return errors.New("提权进程句柄为空")
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, uint32(5*time.Minute/time.Millisecond)); err != nil {
		return fmt.Errorf("等待提权进程: %w", err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("提权动作失败，退出码 %d", code)
	}
	return nil
}

// spawnDetached 启动 `rsagent deploy <action>` 子进程但不等待。
func spawnDetached(o Options, action string, elevate bool) error {
	args := []string{"deploy", action, "--data-dir", o.DataDir}
	if o.Listen != "" {
		args = append(args, "--listen", o.Listen)
	}
	if !elevate {
		cmd := exec.Command(o.ExePath, args...)
		cmd.Dir = filepath.Dir(o.ExePath)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
		return cmd.Start()
	}
	params := make([]string, 0, len(args))
	for _, a := range args {
		params = append(params, quoteArg(a))
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(o.ExePath)
	parm, _ := windows.UTF16PtrFromString(strings.Join(params, " "))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(o.ExePath))
	if err := windows.ShellExecute(0, verb, file, parm, dir, windows.SW_HIDE); err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == 1223 {
			return errors.New("用户取消了 UAC 提权")
		}
		return fmt.Errorf("提权启动失败: %w", err)
	}
	return nil
}

// ---- 工具 ----

// runHidden 运行命令行工具，不弹窗，返回合并输出。
func runHidden(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// decodeConsole 把 schtasks、netsh 的本地代码页输出尽量转成 UTF-8。
func decodeConsole(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(u))
	}
	// schtasks /XML 输出是 UTF-16 带 BOM，其他命令是 ANSI 代码页；
	// 用 MultiByteToWideChar 按当前代码页解码。
	if len(b) == 0 {
		return ""
	}
	const cpACP = 0 // 当前系统 ANSI 代码页
	n, err := windows.MultiByteToWideChar(cpACP, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return string(b)
	}
	u := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(cpACP, 0, &b[0], int32(len(b)), &u[0], n); err != nil {
		return string(b)
	}
	return windows.UTF16ToString(u)
}

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(u)*2)
	out = append(out, 0xFF, 0xFE)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
