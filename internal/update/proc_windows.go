//go:build windows

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// WaitForExit 等待指定进程退出，超时或进程不存在都返回。
func WaitForExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
}

// SpawnDetached 启动一个不随本进程退出的子进程。
func SpawnDetached(exe string, args []string, extraEnv []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	return cmd.Start()
}

// RestartSelf 用同样的参数启动新实例，新实例会先等本进程退出；调用方随后应自行退出。
func RestartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return SpawnDetached(exe, os.Args[1:], []string{"RSAGENT_WAIT_PID=" + strconv.Itoa(os.Getpid())})
}
