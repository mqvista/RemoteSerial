package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths 是数据目录下的固定布局。
//
// 单进程模式和服务模式共用同一目录，切换模式不迁移状态。
type Paths struct {
	DataDir    string // 根目录
	ConfigFile string // config.json
	StateDir   string // tsstate，tsnet 节点状态，含私钥
	LogDir     string // logs
}

// ResolvePaths 根据覆盖值或平台默认值得到目录布局。
func ResolvePaths(override string) Paths {
	dir := override
	if dir == "" {
		dir = DefaultDataDir()
	}
	dir = filepath.Clean(dir)
	return Paths{
		DataDir:    dir,
		ConfigFile: filepath.Join(dir, "config.json"),
		StateDir:   filepath.Join(dir, "tsstate"),
		LogDir:     filepath.Join(dir, "logs"),
	}
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
	default:
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
