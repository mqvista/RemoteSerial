//go:build !windows

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// WaitForExit 等待指定进程退出，超时或进程不存在都返回。
func WaitForExit(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// SpawnDetached 启动一个不随本进程退出的子进程。
func SpawnDetached(exe string, args []string, extraEnv []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// RestartSelf 用 exec 原地替换进程映像，PID 不变，systemd 不会察觉。
// 成功时不返回。
func RestartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
