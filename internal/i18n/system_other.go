//go:build !windows && !darwin

package i18n

import (
	"os"
	"os/exec"
	"strings"
)

func systemLanguage() string {
	// OpenWrt：跟随 LuCI 的语言设置（luci.main.lang，如 zh_tw、en）；auto 表示跟随浏览器，
	// 这时由 LuCI 页面打开时告诉 rsagent（POST /api/language/follow）
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		if out, err := exec.Command("uci", "-q", "get", "luci.main.lang").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" && v != "auto" {
				return v
			}
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return v
		}
	}
	return ""
}
