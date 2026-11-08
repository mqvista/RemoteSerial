//go:build windows

// Package shell 是远端本机的桌面壳：托盘图标加 webview 窗口，窗口里加载本机管理界面。
//
// 线程模型：webview 窗口必须在创建它的线程上跑消息循环，所以 Run 占用调用它的
// goroutine 并锁定 OS 线程；托盘在另一条锁定的线程上跑自己的消息循环。
// 关闭窗口只是回到托盘，托盘菜单"退出"才结束进程。
package shell

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"rsagent/internal/i18n"
)

//go:embed icon.ico
var iconICO []byte

// Options 是壳层参数。
type Options struct {
	Listen     string // core 的本机监听地址
	DataDir    string
	Log        *slog.Logger
	Background bool // 启动时不弹出窗口
}

// Shell 是托盘加窗口的组合。
type Shell struct {
	o        Options
	openCh   chan struct{}
	quitCh   chan struct{}
	quitOnce sync.Once

	mu          sync.Mutex
	wv          webview2.WebView
	noWebView2  bool
	statusTitle string
	items       *trayItems
	poke        chan struct{} // 语言变化后立即刷新状态行
}

type trayItems struct {
	open, status, browser, data, quit *systray.MenuItem
}

// New 创建壳层，不启动。
func New(o Options) *Shell {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Shell{o: o, openCh: make(chan struct{}, 1), quitCh: make(chan struct{}), poke: make(chan struct{}, 1)}
}

// Open 打开或前置管理窗口，可从任意 goroutine 调用。
func (s *Shell) Open() {
	s.mu.Lock()
	wv := s.wv
	s.mu.Unlock()
	if wv != nil {
		bringToFront(uintptr(wv.Window()))
		return
	}
	select {
	case s.openCh <- struct{}{}:
	default:
	}
}

// Quit 关闭窗口与托盘并让 Run 返回。
func (s *Shell) Quit() {
	s.quitOnce.Do(func() {
		close(s.quitCh)
		s.mu.Lock()
		wv := s.wv
		s.mu.Unlock()
		if wv != nil {
			wv.Destroy()
		}
	})
}

// Run 必须在主 goroutine 调用，阻塞到退出。
func (s *Shell) Run(ctx context.Context) error {
	runtime.LockOSThread()
	enableDPIAwareness()
	baseURL := "http://" + s.o.Listen + "/"
	i18n.OnChange(func(i18n.Lang) {
		s.applyTrayLabels()
		s.mu.Lock()
		wv := s.wv
		s.mu.Unlock()
		if wv != nil {
			wv.Dispatch(func() { wv.SetTitle(i18n.T("rsagent 远程串口")) })
		}
	})
	go s.runTray(baseURL)
	if !s.o.Background {
		s.Open()
	}
	for {
		select {
		case <-ctx.Done():
			s.Quit()
			systray.Quit()
			return nil
		case <-s.quitCh:
			systray.Quit()
			return nil
		case <-s.openCh:
			s.showWindow(baseURL)
		}
	}
}

func (s *Shell) showWindow(url string) {
	s.mu.Lock()
	noWV := s.noWebView2
	s.mu.Unlock()
	if noWV {
		openBrowser(url)
		return
	}

	dataPath := filepath.Join(os.Getenv("LOCALAPPDATA"), "RemoteSerial", "webview2")
	_ = os.MkdirAll(dataPath, 0o755)

	var wv webview2.WebView
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.o.Log.Error(i18n.T("创建 WebView2 窗口时崩溃"), "err", fmt.Sprint(r))
				wv = nil
			}
		}()
		wv = webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:  dataPath,
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title: i18n.T("rsagent 远程串口"),
				// 声明 DPI 感知后尺寸按物理像素计，换算后与 100% 缩放下观感一致
				Width:  scaled(1100),
				Height: scaled(720),
				Center: true,
			},
		})
	}()
	if wv == nil {
		s.mu.Lock()
		s.noWebView2 = true
		s.mu.Unlock()
		s.o.Log.Error(i18n.T("无法创建 WebView2 窗口，可能缺少 WebView2 运行时，改用系统浏览器"))
		messageBox("rsagent", i18n.T("未找到 WebView2 运行时，将改用系统浏览器打开管理界面。\n可从微软官网安装 Evergreen 运行时后重启 rsagent。"))
		openBrowser(url)
		return
	}

	s.mu.Lock()
	s.wv = wv
	s.mu.Unlock()
	setWindowIcon(uintptr(wv.Window()))
	wv.Navigate(url)
	wv.Run()
	s.mu.Lock()
	s.wv = nil
	s.mu.Unlock()
}

// ---- 托盘 ----

func (s *Shell) runTray(baseURL string) {
	runtime.LockOSThread()
	systray.Run(func() {
		systray.SetIcon(iconICO)
		it := &trayItems{}
		it.open = systray.AddMenuItem("", "")
		it.status = systray.AddMenuItem("", "")
		it.status.Disable()
		systray.AddSeparator()
		it.browser = systray.AddMenuItem("", "")
		it.data = systray.AddMenuItem("", "")
		systray.AddSeparator()
		it.quit = systray.AddMenuItem("", "")
		s.mu.Lock()
		s.items = it
		s.mu.Unlock()
		s.applyTrayLabels()
		mOpen, mStatus, mBrowser, mData, mQuit := it.open, it.status, it.browser, it.data, it.quit

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					s.Open()
				case <-mBrowser.ClickedCh:
					openBrowser(baseURL)
				case <-mData.ClickedCh:
					_ = exec.Command("explorer.exe", s.o.DataDir).Start()
				case <-mQuit.ClickedCh:
					s.Quit()
					return
				case <-s.quitCh:
					return
				}
			}
		}()
		go s.statusLoop(baseURL, mStatus)
	}, func() {})
}

// applyTrayLabels 按当前语言设置托盘菜单文字。
func (s *Shell) applyTrayLabels() {
	s.mu.Lock()
	it := s.items
	s.statusTitle = ""
	s.mu.Unlock()
	if it == nil {
		return
	}
	systray.SetTooltip(i18n.T("rsagent 远程串口"))
	it.open.SetTitle(i18n.T("打开控制台"))
	it.open.SetTooltip(i18n.T("打开管理界面窗口"))
	it.status.SetTitle(i18n.T("状态：读取中…"))
	it.browser.SetTitle(i18n.T("在浏览器中打开"))
	it.browser.SetTooltip(i18n.T("用系统浏览器打开管理界面"))
	it.data.SetTitle(i18n.T("打开数据目录"))
	it.quit.SetTitle(i18n.T("退出"))
	it.quit.SetTooltip(i18n.T("退出托盘与窗口；以服务方式运行的 core 不受影响"))
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

type statusLite struct {
	Language  string `json:"language"`
	Mode      string `json:"mode"`
	Tailscale struct {
		State string `json:"state"`
		IP    string `json:"ip"`
	} `json:"tailscale"`
	Bridges []struct {
		State string `json:"state"`
	} `json:"bridges"`
}

func (s *Shell) statusLoop(baseURL string, item *systray.MenuItem) {
	client := &http.Client{Timeout: 3 * time.Second}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		title := i18n.T("状态：core 未响应")
		if resp, err := client.Get(baseURL + "api/status"); err == nil {
			var st statusLite
			if json.NewDecoder(resp.Body).Decode(&st) == nil {
				// core 可能在另一个进程（服务模式），跟随它的语言
				if l := i18n.Normalize(st.Language); l != "" && l != i18n.Current() {
					i18n.SetLang(l)
				}
				connected := 0
				for _, b := range st.Bridges {
					if b.State == "Connected" {
						connected++
					}
				}
				ts := map[string]string{
					"NotConfigured": i18n.T("未登录"), "Starting": i18n.T("连接中"), "NeedsLogin": i18n.T("需要登录"),
					"NeedsMachineAuth": i18n.T("等待批准"), "Running": i18n.T("在线"), "Stopped": i18n.T("已停止"), "Error": i18n.T("错误"),
				}[st.Tailscale.State]
				if ts == "" {
					ts = st.Tailscale.State
				}
				if st.Tailscale.IP != "" {
					ts += " " + st.Tailscale.IP
				}
				mode := map[string]string{"standalone": i18n.T("单进程"), "service": i18n.T("服务")}[st.Mode]
				if mode == "" {
					mode = st.Mode
				}
				title = i18n.Sprintf("[%s] Tailscale %s · 桥接 %d/%d 已连接", mode, ts, connected, len(st.Bridges))
			}
			resp.Body.Close()
		}
		s.mu.Lock()
		changed := title != s.statusTitle
		s.statusTitle = title
		s.mu.Unlock()
		if changed {
			item.SetTitle(title)
		}
		select {
		case <-s.quitCh:
			return
		case <-tick.C:
		case <-s.poke:
		}
	}
}

// ---- Win32 小工具 ----

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procCreateIconFromResEx = user32.NewProc("CreateIconFromResourceEx")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
)

func openBrowser(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	_ = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	b, _ := windows.UTF16PtrFromString(text)
	_, _ = windows.MessageBox(0, b, t, windows.MB_OK|windows.MB_ICONWARNING)
}

func bringToFront(hwnd uintptr) {
	const swRestore = 9
	_, _, _ = procShowWindow.Call(hwnd, swRestore)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

// setWindowIcon 把嵌入的 ICO 里的 32 和 16 像素图设为窗口图标。
func setWindowIcon(hwnd uintptr) {
	const (
		wmSetIcon = 0x0080
		iconSmall = 0
		iconBig   = 1
		lrDefault = 0x00000000
		verNT     = 0x00030000
	)
	for _, e := range []struct {
		size  int
		which uintptr
	}{{32, iconBig}, {16, iconSmall}} {
		img := icoEntry(iconICO, e.size)
		if len(img) == 0 {
			continue
		}
		h, _, _ := procCreateIconFromResEx.Call(
			uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)), 1, verNT,
			uintptr(e.size), uintptr(e.size), lrDefault,
		)
		if h != 0 {
			_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, e.which, h)
		}
	}
}

// icoEntry 从 ICO 容器里取出指定边长的图像数据（PNG 或 DIB）。
func icoEntry(ico []byte, size int) []byte {
	if len(ico) < 6 {
		return nil
	}
	n := int(binary.LittleEndian.Uint16(ico[4:6]))
	for i := 0; i < n; i++ {
		off := 6 + 16*i
		if off+16 > len(ico) {
			return nil
		}
		w := int(ico[off])
		if w == 0 {
			w = 256
		}
		if w != size {
			continue
		}
		length := int(binary.LittleEndian.Uint32(ico[off+8 : off+12]))
		start := int(binary.LittleEndian.Uint32(ico[off+12 : off+16]))
		if start+length > len(ico) {
			return nil
		}
		return ico[start : start+length]
	}
	return nil
}
