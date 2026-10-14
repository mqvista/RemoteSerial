//go:build !windows

package deploy

import (
	"errors"
	"fmt"
	"os"
)

func isElevated() bool { return os.Geteuid() == 0 }

func fillStatus(st *Status, o Options) {
	st.Elevated = isElevated()
	st.Task = TaskStatus{Supported: false}
	st.Service = serviceStatus(o)
	st.Firewall = FirewallStatus{Supported: false, UDPPort: o.UDPPort}
	st.WebView2 = WebView2Status{Supported: false}
	st.AutoLogo = AutoLogon{Supported: false}
}

func runAction(o Options, action string) error {
	switch action {
	case ActionServiceInstall, ActionServiceRemove, ActionServiceStart, ActionServiceStop:
		return serviceAction(o, action)
	case ActionTaskInstall, ActionTaskRemove, ActionFirewallAdd, ActionFirewallRemove, ActionWebView2Install:
		return fmt.Errorf("动作 %q 仅支持 Windows", action)
	}
	return fmt.Errorf("未知动作 %q", action)
}

func elevateAndWait(Options, string) error {
	return errors.New("需要 root 权限：请用 sudo 运行 rsagent deploy <action>")
}

// updateServiceBinPath 在 systemd 上重写单元文件即可，没有删除延迟问题。
func updateServiceBinPath(o Options) error {
	svc, err := newControlService(o)
	if err != nil {
		return err
	}
	_ = svc.Uninstall()
	return svc.Install()
}
