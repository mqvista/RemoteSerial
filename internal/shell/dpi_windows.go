//go:build windows

package shell

import "golang.org/x/sys/windows"

// 未声明 DPI 感知的程序在 125%、150% 等缩放下会被系统按 96 DPI 渲染后整体拉伸，
// 窗口和托盘菜单因此发糊。壳层启动时（创建任何窗口之前）声明为 Per-Monitor V2，
// 由 WebView2 按实际 DPI 渲染页面。

var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext") // Windows 10 1703+
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")            // Vista+，仅系统 DPI
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")               // Windows 10 1607+
	shcore                            = windows.NewLazySystemDLL("shcore.dll")
	procSetProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness") // Windows 8.1+
	procGetDeviceCaps                 = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetDeviceCaps")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
)

const (
	dpiAwarenessContextPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	processPerMonitorDPIAware       = 2
	logPixelsX                      = 88
)

// enableDPIAwareness 按系统支持程度依次尝试，较新的接口优先。
func enableDPIAwareness() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		if r, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorV2); r != 0 {
			return
		}
	}
	if procSetProcessDpiAwareness.Find() == nil {
		// 返回 S_OK(0) 或 E_ACCESSDENIED（已设置过）都算完成
		if r, _, _ := procSetProcessDpiAwareness.Call(processPerMonitorDPIAware); r == 0 || uint32(r) == 0x80070005 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}

// systemDPI 返回主显示器的 DPI，失败时为 96。
func systemDPI() uint {
	if procGetDpiForSystem.Find() == nil {
		if r, _, _ := procGetDpiForSystem.Call(); r > 0 {
			return uint(r)
		}
	}
	if procGetDC.Find() == nil && procGetDeviceCaps.Find() == nil {
		if dc, _, _ := procGetDC.Call(0); dc != 0 {
			r, _, _ := procGetDeviceCaps.Call(dc, logPixelsX)
			procReleaseDC.Call(0, dc)
			if r > 0 {
				return uint(r)
			}
		}
	}
	return 96
}

// scaled 把以 96 DPI 为基准的尺寸换算成当前显示器的像素。
func scaled(px uint) uint { return px * systemDPI() / 96 }
