package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/deploy"
	"rsagent/internal/shell"
)

// RunUI 以桌面模式运行：主 goroutine 跑托盘与窗口，后台的看护循环保证本机总有一个 core 可用。
//
// 规则：
//   - 本机已有带壳的实例：唤起它的窗口后退出，实现单实例。
//   - 本机有 core 在响应（服务或别的 run 进程）：只跑壳。
//   - 没有 core 响应，且服务未安装或已停止：在本进程启动 core。
//   - 本进程 core 因服务接管而退位后，壳继续运行并连接服务；服务再被卸载或停止时，看护循环会重新起 core。
func RunUI(ctx context.Context, o Options) error {
	paths := config.ResolvePaths(o.DataDir)
	if err := paths.Ensure(); err != nil {
		return fmt.Errorf("创建数据目录 %s: %w", paths.DataDir, err)
	}
	cfg, _, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	listen := cfg.Listen
	if o.Listen != "" {
		listen = o.Listen
	}
	if probeCore(listen) == coreWithShell {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sh := shell.New(shell.Options{Listen: listen, DataDir: paths.DataDir})
	sup := &supervisor{opts: o, listen: listen, dataDir: paths.DataDir, show: sh.Open}
	go sup.loop(ctx)

	err = sh.Run(ctx)
	cancel()
	sup.wait(5 * time.Second)
	return err
}

// supervisor 负责按需启动和回收本进程内的 core。
type supervisor struct {
	opts    Options
	listen  string
	dataDir string
	show    func()

	mu         sync.Mutex
	cancelCore context.CancelFunc
	coreDone   chan error
}

func (s *supervisor) loop(ctx context.Context) {
	missingSince := time.Time{}
	const fallbackAfter = 8 * time.Second
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		s.reapCore()
		if !s.coreRunning() {
			if coreResponds(s.listen) {
				missingSince = time.Time{}
			} else {
				if missingSince.IsZero() {
					missingSince = time.Now()
				}
				// 首次启动不等；之后等一段时间，给服务崩溃自动重启留出余地。
				if first || time.Since(missingSince) >= fallbackAfter {
					if s.shouldStartCore() {
						s.startCore(ctx)
						missingSince = time.Time{}
					}
				}
			}
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// shouldStartCore 判断没有 core 响应时是否该由本进程来起：服务未安装或已停止才起。
func (s *supervisor) shouldStartCore() bool {
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	st := deploy.GetStatus(deploy.Options{ExePath: exe, DataDir: s.dataDir, Listen: s.listen})
	if st.Service.Installed && st.Service.Running {
		return false
	}
	return true
}

func (s *supervisor) coreRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCore != nil
}

// reapCore 回收已退出的 core（例如服务接管后退位）。
func (s *supervisor) reapCore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coreDone == nil {
		return
	}
	select {
	case <-s.coreDone:
		s.cancelCore = nil
		s.coreDone = nil
	default:
	}
}

func (s *supervisor) startCore(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelCore != nil {
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	o := s.opts
	o.Mode = "standalone"
	o.ShowUI = s.show
	o.Handoff = cancel
	done := make(chan error, 1)
	go func() { done <- Run(cctx, o) }()
	s.cancelCore = cancel
	s.coreDone = done
}

func (s *supervisor) wait(timeout time.Duration) {
	s.mu.Lock()
	cancel := s.cancelCore
	done := s.coreDone
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
}

type coreState int

const (
	coreNone      coreState = iota // 端口无人监听
	coreHeadless                   // 有 core 但没有壳（服务模式或 run 子命令）
	coreWithShell                  // 有带壳的实例，且已唤起其窗口
)

// probeCore 用 POST /api/ui/show 探测：200 表示已有壳并已唤起窗口，404 表示只有 core。
func probeCore(listen string) coreState {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post("http://"+listen+"/api/ui/show", "application/json", nil)
	if err != nil {
		return coreNone
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return coreWithShell
	}
	return coreHeadless
}

func coreResponds(listen string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + listen + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
