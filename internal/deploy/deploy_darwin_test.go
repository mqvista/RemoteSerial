//go:build darwin

package deploy

import (
	"os/exec"
	"strings"
	"testing"
)

// 授权框执行的命令要经过 AppleScript 字符串和 /bin/sh 两层解析，路径里的空格、引号都不能走样。
func TestDeployCommandQuoting(t *testing.T) {
	o := Options{
		ExePath: "/bin/echo",
		DataDir: `/Users/x/Library/Application Support/Remote "Serial" it's`,
		Listen:  "127.0.0.1:7890",
	}
	script := "do shell script " + appleScriptString(deployCommand(o, ActionServiceInstall))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("osascript: %v: %s", err, out)
	}
	want := "deploy service-install --data-dir " + o.DataDir + " --listen 127.0.0.1:7890"
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestServiceConfigRunsAsDataDirOwner(t *testing.T) {
	dir := t.TempDir()
	c := ServiceConfig(Options{DataDir: dir, ExePath: "/bin/true"})
	if c.UserName == "" || c.EnvVars["HOME"] == "" {
		t.Fatalf("服务应以数据目录属主身份运行: UserName=%q EnvVars=%v", c.UserName, c.EnvVars)
	}
	if c.Option["RunAtLoad"] != true || c.Option["KeepAlive"] != true {
		t.Fatalf("launchd 选项缺失: %v", c.Option)
	}
	if !strings.HasPrefix(c.Option["LogDirectory"].(string), dir) {
		t.Fatalf("日志应写到数据目录: %v", c.Option["LogDirectory"])
	}
}

func TestAdminErrorCancel(t *testing.T) {
	err := adminError([]byte("0:1: execution error: User canceled. (-128)"), nil)
	if err == nil || strings.Contains(err.Error(), "-128") {
		t.Fatalf("取消授权应给出明确提示: %v", err)
	}
}
