//go:build darwin

package i18n

import (
	"os"
	"os/exec"
	"strings"
)

// systemLanguage 读取系统偏好语言列表的第一项。从 Finder 启动的 .app 没有 LANG 环境变量。
func systemLanguage() string {
	if v := os.Getenv("LC_ALL"); v != "" {
		return v
	}
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return os.Getenv("LANG")
	}
	// 输出形如 ( "zh-Hant-TW", "en-US" )
	for _, f := range strings.FieldsFunc(string(out), func(r rune) bool { return r == '(' || r == ')' || r == ',' || r == '\n' }) {
		if f = strings.Trim(strings.TrimSpace(f), `"`); f != "" {
			return f
		}
	}
	return ""
}
