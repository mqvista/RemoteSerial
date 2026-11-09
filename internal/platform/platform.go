// Package platform 识别运行环境中与平台相关、但不能靠 GOOS 区分的差异，目前只有 OpenWrt。
//
// OpenWrt 与普通 Linux 用同一个程序（静态编译，不依赖 libc），启动时按
// /etc/openwrt_release 判断，路径、服务管理和网络后端的默认值随之不同。
package platform

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// EnvPlatform 可强制指定平台（目前只认 "openwrt"），用于在其他系统上测试 OpenWrt 分支。
const EnvPlatform = "RSAGENT_PLATFORM"

const releaseFile = "/etc/openwrt_release"

var (
	once    sync.Once
	openwrt bool
	release map[string]string
)

func detect() {
	once.Do(func() {
		release = map[string]string{}
		if f, err := os.Open(releaseFile); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
				if ok {
					release[k] = strings.Trim(v, `'"`)
				}
			}
			openwrt = runtime.GOOS == "linux"
		}
		if strings.EqualFold(os.Getenv(EnvPlatform), "openwrt") {
			openwrt = true
		}
	})
}

// OpenWrt 判断是否运行在 OpenWrt 上。
func OpenWrt() bool {
	detect()
	return openwrt
}

// OpenWrtRelease 返回 OpenWrt 版本，如 "24.10.4"；非 OpenWrt 或读不到时为空。
func OpenWrtRelease() string {
	detect()
	return release["DISTRIB_RELEASE"]
}

// Name 返回给界面显示的平台名：openwrt，或 GOOS。
func Name() string {
	if OpenWrt() {
		return "openwrt"
	}
	return runtime.GOOS
}

// PkgManager 返回 OpenWrt 的套件管理器："apk"（25.x 起）或 "opkg"（24.10 及更早），找不到时为空。
func PkgManager() string {
	if _, err := exec.LookPath("apk"); err == nil {
		return "apk"
	}
	if _, err := exec.LookPath("opkg"); err == nil {
		return "opkg"
	}
	return ""
}

// InstallCommand 返回用套件管理器安装 pkg 的命令行，供界面显示和执行。
func InstallCommand(mgr, pkg string) string {
	switch mgr {
	case "apk":
		return "apk update && apk add " + pkg
	case "opkg":
		return "opkg update && opkg install " + pkg
	}
	return ""
}

// PackageInstalled 判断 OpenWrt 套件 name 是否由套件管理器安装，返回管理器名（opkg | apk），否则为空。
// 直接读套件数据库，不调用外部命令。
func PackageInstalled(name string) string {
	if !OpenWrt() {
		return ""
	}
	if _, err := os.Stat("/usr/lib/opkg/info/" + name + ".control"); err == nil {
		return "opkg"
	}
	if b, err := os.ReadFile("/lib/apk/db/installed"); err == nil {
		if strings.Contains("\n"+string(b), "\nP:"+name+"\n") {
			return "apk"
		}
	}
	return ""
}

// InstallScriptURL 是一键安装脚本。在 OpenWrt 上它会用 .ipk / .apk 安装，已用套件安装时再执行一次即升级。
const InstallScriptURL = "https://raw.githubusercontent.com/mqvista/RemoteSerial/main/deploy/install.sh"

// UpgradeCommand 返回升级 rsagent 套件的命令。rsagent 的套件不在 OpenWrt 官方源里，opkg / apk upgrade 找不到新版，
// 所以改为再次执行安装脚本：它从 GitHub Releases 下载对应的新套件，交给套件管理器安装。
func UpgradeCommand(mgr, name string) string {
	if mgr == "" || name != "rsagent" {
		return ""
	}
	return "wget -qO- " + InstallScriptURL + " | sh"
}

// OverrideForTest 强制 OpenWrt 判断结果，返回恢复函数。只用于测试。
func OverrideForTest(isOpenWrt bool) (restore func()) {
	detect()
	prev := openwrt
	openwrt = isOpenWrt
	return func() { openwrt = prev }
}
