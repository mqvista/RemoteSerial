//go:build !windows

package shell

// AttachParentConsole 在非 Windows 平台无操作。
func AttachParentConsole() {}
