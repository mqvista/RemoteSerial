//go:build darwin

// Package shell 是远端本机的桌面壳：菜单栏图标加 WebKit 窗口，窗口里加载本机管理界面。
//
// 线程模型：AppKit 只能在主线程使用。systray.Run 在主线程跑 [NSApp run]，
// 窗口由 window_darwin.m 通过 dispatch 到主队列创建和显示，所以 Open 可从任意 goroutine 调用。
// 关闭窗口只是回到菜单栏，菜单"退出"或 Cmd+Q 才结束进程。
package shell

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include <pthread.h>
#include "window_darwin.h"
*/
import "C"

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"

	"fyne.io/systray"

	"rsagent/internal/i18n"
)

//go:embed icon_tray.png
var iconTray []byte

// Options 是壳层参数。
type Options struct {
	Listen  string // core 的本机监听地址
	DataDir string
	Log     *slog.Logger
}

// Shell 是菜单栏图标加窗口的组合。
type Shell struct {
	o        Options
	baseURL  string
	quitCh   chan struct{}
	quitOnce sync.Once
	ready    chan struct{}

	mu          sync.Mutex
	statusTitle string
	items       *trayItems
	poke        chan struct{} // 语言变化后立即刷新状态行
}

type trayItems struct {
	open, status, browser, data, quit *systray.MenuItem
}

// current 供 Objective-C 回调（Cmd+Q）找到壳层。
var current struct {
	sync.Mutex
	s *Shell
}

// New 创建壳层，不启动。
func New(o Options) *Shell {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Shell{
		o:       o,
		baseURL: "http://" + o.Listen + "/",
		quitCh:  make(chan struct{}),
		ready:   make(chan struct{}),
		poke:    make(chan struct{}, 1),
	}
}

// Open 打开或前置管理窗口，可从任意 goroutine 调用。
func (s *Shell) Open() {
	select {
	case <-s.quitCh:
		return
	default:
	}
	u := C.CString(s.baseURL)
	defer C.free(unsafe.Pointer(u))
	C.rsShowWindow(u)
}

// Quit 关闭窗口与菜单栏图标并让 Run 返回。
func (s *Shell) Quit() {
	s.quitOnce.Do(func() {
		close(s.quitCh)
		go func() {
			// 事件循环起来之前调用 systray.Quit 无效，等它就绪再停。
			<-s.ready
			systray.Quit()
		}()
	})
}

//export goShellQuit
func goShellQuit() {
	current.Lock()
	s := current.s
	current.Unlock()
	if s != nil {
		s.Quit()
	}
}

// Run 必须在主 goroutine 调用（systray 的 init 已把它锁在主线程），阻塞到退出。
func (s *Shell) Run(ctx context.Context) error {
	if C.pthread_main_np() == 0 {
		return i18n.New("桌面壳必须在主线程运行")
	}
	current.Lock()
	current.s = s
	current.Unlock()
	setNativeLabels()
	i18n.OnChange(func(i18n.Lang) {
		setNativeLabels()
		s.applyTrayLabels()
	})

	go func() {
		select {
		case <-ctx.Done():
			s.Quit()
		case <-s.quitCh:
		}
	}()
	systray.Run(s.onReady, nil)
	return nil
}

func (s *Shell) onReady() {
	systray.SetIcon(iconTray)
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
	close(s.ready)

	s.Open()
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				s.Open()
			case <-mBrowser.ClickedCh:
				_ = exec.Command("open", s.baseURL).Start()
			case <-mData.ClickedCh:
				_ = exec.Command("open", s.o.DataDir).Start()
			case <-mQuit.ClickedCh:
				s.Quit()
				return
			case <-s.quitCh:
				return
			}
		}
	}()
	go s.statusLoop(mStatus)
}

// applyTrayLabels 按当前语言设置菜单文字。
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
	it.quit.SetTooltip(i18n.T("退出菜单栏图标与窗口；以服务方式运行的 core 不受影响"))
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// setNativeLabels 把窗口标题与应用菜单的文字交给 Objective-C 侧，按顺序用 \x1f 分隔。
func setNativeLabels() {
	labels := []string{
		i18n.T("rsagent 远程串口"),
		i18n.T("隐藏 rsagent"), i18n.T("退出 rsagent"),
		i18n.T("编辑"), i18n.T("撤销"), i18n.T("重做"), i18n.T("剪切"), i18n.T("拷贝"), i18n.T("粘贴"), i18n.T("全选"),
		i18n.T("显示"), i18n.T("重新载入"),
		i18n.T("窗口"), i18n.T("最小化"), i18n.T("关闭窗口"),
		i18n.T("确定"), i18n.T("取消"),
	}
	cs := C.CString(strings.Join(labels, "\x1f"))
	defer C.free(unsafe.Pointer(cs))
	C.rsSetLabels(cs)
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

func (s *Shell) statusLoop(item *systray.MenuItem) {
	client := &http.Client{Timeout: 3 * time.Second}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		title := i18n.T("状态：core 未响应")
		if resp, err := client.Get(s.baseURL + "api/status"); err == nil {
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
