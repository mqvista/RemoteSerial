package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/shell"
)

// RunUI 以桌面模式运行：探测本机是否已有 core，没有就在本进程启动 core，
// 然后在主 goroutine 跑托盘与窗口，直到用户退出。
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

	mode := "standalone"
	switch probeCore(listen) {
	case coreWithShell:
		// 已有带托盘的实例，唤起它的窗口后退出，实现单实例。
		return nil
	case coreHeadless:
		mode = "shell"
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sh := shell.New(shell.Options{Listen: listen, DataDir: paths.DataDir, Mode: mode})
	coreErr := make(chan error, 1)
	coreCtx, cancelCore := context.WithCancel(ctx)
	defer cancelCore()
	if mode == "standalone" {
		o.ShowUI = sh.Open
		o.Mode = "standalone"
		o.Handoff = cancelCore // 部署页安装服务后，本进程 core 退位，壳保留
		go func() { coreErr <- Run(coreCtx, o) }()
		if err := waitCore(ctx, listen, 15*time.Second, coreErr); err != nil {
			return err
		}
	}

	err = sh.Run(ctx)
	cancel()
	if mode == "standalone" {
		select {
		case <-coreErr:
		case <-time.After(5 * time.Second):
		}
	}
	return err
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

func waitCore(ctx context.Context, listen string, timeout time.Duration, coreErr <-chan error) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-coreErr:
			if err == nil {
				err = errors.New("core 提前退出")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if resp, err := client.Get("http://" + listen + "/api/version"); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("core 启动超时")
}
