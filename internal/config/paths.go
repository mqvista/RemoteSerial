package config

import (
	"os"
	"path/filepath"
	"runtime"

	"rsagent/internal/platform"
)

// Paths 是数据目录下的固定布局。
//
// 单进程模式和服务模式共用同一目录，切换模式不迁移状态。
type Paths struct {
	DataDir    string // 根目录
	ConfigFile string // config.json
	StateDir   string // tsstate，tsnet 节点状态，含私钥
	LogDir     string // logs；也存放串口录制文件
}

// OpenWrtRuntimeDir 是 OpenWrt 上存放日志与录制的目录：位于内存（tmpfs），避免磨损闪存，重启后清空。
const OpenWrtRuntimeDir = "/tmp/rsagent"

// ResolvePaths 根据覆盖值或平台默认值得到目录布局。
func ResolvePaths(override string) Paths {
	dir := override
	if dir == "" {
		dir = DefaultDataDir()
	}
	dir = filepath.Clean(dir)
	p := Paths{
		DataDir:    dir,
		ConfigFile: filepath.Join(dir, "config.json"),
		StateDir:   filepath.Join(dir, "tsstate"),
		LogDir:     filepath.Join(dir, "logs"),
	}
	if platform.OpenWrt() {
		p.LogDir = filepath.Join(OpenWrtRuntimeDir, "logs")
	}
	return p
}

// Ensure 创建全部目录。
func (p Paths) Ensure() error {
	for _, d := range []string{p.DataDir, p.StateDir, p.LogDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// DefaultDataDir 返回平台默认数据目录。
//
//   - Windows：%ProgramData%\RemoteSerial
//   - macOS：~/Library/Application Support/RemoteSerial（root 时为 /Library/Application Support/RemoteSerial）
//   - OpenWrt 且为 root：/etc/rsagent（随固件升级保留，见 /lib/upgrade/keep.d/rsagent）
//   - Linux 且为 root：/var/lib/rsagent
//   - 其他：$XDG_DATA_HOME/rsagent 或 ~/.local/share/rsagent
func DefaultDataDir() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "RemoteSerial")
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil && home != "" && os.Geteuid() != 0 {
			return filepath.Join(home, "Library", "Application Support", "RemoteSerial")
		}
		return "/Library/Application Support/RemoteSerial"
	default:
		if os.Geteuid() == 0 && platform.OpenWrt() {
			return "/etc/rsagent"
		}
		if os.Geteuid() == 0 {
			return "/var/lib/rsagent"
		}
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, "rsagent")
		}
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "/var/lib/rsagent"
		}
		return filepath.Join(home, ".local", "share", "rsagent")
	}
}
