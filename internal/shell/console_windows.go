//go:build windows

package shell

import (
	"os"

	"golang.org/x/sys/windows"
)

// AttachParentConsole 让以 windowsgui 子系统构建的程序在终端里运行时仍能输出。
// 已有控制台（普通控制台构建）时 AttachConsole 失败，直接返回。
func AttachParentConsole() {
	const attachParentProcess = ^uintptr(0)
	k32 := windows.NewLazySystemDLL("kernel32.dll")
	r, _, _ := k32.NewProc("AttachConsole").Call(attachParentProcess)
	if r == 0 {
		return
	}
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 {
		os.Stdout = os.NewFile(uintptr(h), "stdout")
	}
	if h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE); err == nil && h != 0 {
		os.Stderr = os.NewFile(uintptr(h), "stderr")
	}
}
