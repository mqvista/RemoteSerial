//go:build darwin

package deploy

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"

	"rsagent/internal/i18n"
	"rsagent/internal/update"
)

// 登录后启动用用户级 LaunchAgent 实现，不需要管理员权限，也不要求正式签名。
// 它用 open -a 打开 .app（交给 LaunchServices，与 launchd 脱钩，卸载时不会顺带结束程序），
// 并带上 --background：登录后只出现菜单栏图标，不弹窗口。
// 以服务方式运行时，服务进程也是这个用户，同样能读写它。
const loginLabel = "com.github.mqvista.rsagent.login"

func loginPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", i18n.New("找不到用户主目录")
	}
	return filepath.Join(home, "Library", "LaunchAgents", loginLabel+".plist"), nil
}

// loginBundle 是登录时要打开的 .app：本程序所在的包；服务的程序副本不在包里，
// 这时沿用已登记的包路径。
func loginBundle(o Options) string {
	if b := update.BundleOf(o.ExePath); b != "" {
		return b
	}
	if p, err := loginPlistPath(); err == nil {
		if b := registeredBundle(p); b != "" {
			return b
		}
	}
	for _, b := range []string{"/Applications/RemoteSerial Agent.app"} {
		if _, err := os.Stat(b); err == nil {
			return b
		}
	}
	return ""
}

type plistDoc struct {
	Dict struct {
		Items []plistItem `xml:",any"`
	} `xml:"dict"`
}

type plistItem struct {
	XMLName xml.Name
	Value   string      `xml:",chardata"`
	Strings []plistItem `xml:"string"`
}

// registeredBundle 从已有的 plist 里取出 open -a 后面的 .app 路径。
func registeredBundle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var d plistDoc
	if xml.Unmarshal(data, &d) != nil {
		return ""
	}
	for _, it := range d.Dict.Items {
		if it.XMLName.Local != "array" {
			continue
		}
		for i, s := range it.Strings {
			if s.Value == "-a" && i+1 < len(it.Strings) {
				return it.Strings[i+1].Value
			}
		}
	}
	return ""
}

func loginStatus(o Options) LoginItem {
	st := LoginItem{Supported: true, Bundle: loginBundle(o)}
	p, err := loginPlistPath()
	if err != nil {
		st.Supported = false
		st.Detail = err.Error()
		return st
	}
	if _, err := os.Stat(p); err == nil {
		st.Enabled = true
		if b := registeredBundle(p); b != "" {
			st.Bundle = b
		}
	}
	if st.Bundle == "" {
		st.Supported = false
		st.Detail = i18n.T("找不到 RemoteSerial Agent.app，请从 .app 运行本程序")
	}
	return st
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func loginEnable(o Options) error {
	bundle := loginBundle(o)
	if bundle == "" {
		return i18n.New("找不到 RemoteSerial Agent.app，请从 .app 运行本程序")
	}
	p, err := loginPlistPath()
	if err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + loginLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/open</string>
		<string>-a</string>
		<string>` + xmlText(bundle) + `</string>
		<string>--args</string>
		<string>ui</string>
		<string>--background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
</dict>
</plist>
`
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(plist), 0o644)
}

// loginDisable 只删除 plist：已加载的条目在注销前保留，但它不常驻，不会再拉起程序。
func loginDisable() error {
	p, err := loginPlistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
