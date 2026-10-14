// Package app 把配置、日志、tsnet 节点、web 服务等组件装配起来，供各子命令复用。
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/buildinfo"
	"rsagent/internal/config"
	"rsagent/internal/logging"
	"rsagent/internal/tsnode"
	"rsagent/internal/update"
	"rsagent/internal/web"
)

// Options 是 run 子命令的运行参数，命令行覆盖配置文件。
type Options struct {
	DataDir  string
	Listen   string
	LogLevel string
	Console  bool
	ShowUI   func() // 桌面模式下由壳层提供，用于单实例唤起窗口
	Mode     string // standalone | service，空为 standalone
	Handoff  func() // 部署页安装服务成功后调用，让本进程的 core 退位给服务
	Restart  func() // 应用更新后重启本进程；空表示不支持
	Quit     func() // 本机 POST /api/ui/quit 时退出本进程
}

// Run 运行 core，阻塞到 ctx 取消。
func Run(ctx context.Context, o Options) error {
	if o.Mode == "" {
		o.Mode = "standalone"
	}
	paths := config.ResolvePaths(o.DataDir)
	if err := paths.Ensure(); err != nil {
		return fmt.Errorf("创建数据目录 %s: %w", paths.DataDir, err)
	}

	cfg, created, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	if o.LogLevel != "" {
		cfg.LogLevel = o.LogLevel
	}
	if o.Listen != "" {
		cfg.Listen = o.Listen
	}
	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return err
	}

	lg, err := logging.Setup(logging.Options{Dir: paths.LogDir, Level: level, Console: o.Console})
	if err != nil {
		return err
	}
	defer lg.Close()

	lg.Info("rsagent 启动", "version", buildinfo.Version, "commit", buildinfo.Commit, "dataDir", paths.DataDir)
	if created {
		lg.Info("已生成默认配置", "file", paths.ConfigFile)
	}

	hub := web.NewHub()
	var srv *web.Server
	var node *tsnode.Node

	bridges := bridge.NewManager(lg.Logger.With("mod", "bridge"), func(ctx context.Context, addr string) string {
		if node == nil {
			return ""
		}
		id, err := node.WhoIs(ctx, addr)
		if err != nil || id == nil {
			return ""
		}
		if id.LoginName != "" && len(id.Tags) == 0 {
			return id.LoginName + "@" + id.Node
		}
		return id.Node
	}, hub.Notify)

	node = tsnode.New(tsnode.Config{
		StateDir:        paths.StateDir,
		Hostname:        cfg.Tailscale.Hostname,
		ControlURL:      cfg.Tailscale.ControlURL,
		UDPPort:         uint16(cfg.Tailscale.UDPPort),
		AdvertiseRoutes: cfg.Tailscale.AdvertiseRoutes,
	}, lg.Logger.With("mod", "tsnode"), tsnode.Hooks{
		OnStarted: func(n *tsnode.Node) {
			addr := fmt.Sprintf(":%d", cfg.Tailscale.WebPort)
			ln, err := n.Listen("tcp", addr)
			if err != nil {
				lg.Error("tailnet 管理界面监听失败", "addr", addr, "err", err)
				return
			}
			lg.Info("tailnet 管理界面已监听", "port", cfg.Tailscale.WebPort)
			go func() {
				if err := srv.Serve(ctx, ln, web.SourceTailnet); err != nil && !errors.Is(err, net.ErrClosed) {
					lg.Debug("tailnet 管理界面监听结束", "err", err)
				}
			}()
			bridges.SetListener(n.Listen)
		},
		OnStopped: func() { bridges.ClearListener() },
		OnChange:  func(tsnode.Snapshot) { hub.Notify() },
	})
	bridges.Apply(cfg.Bridges)

	upd := update.New(update.Options{
		DataDir:   paths.DataDir,
		Current:   buildinfo.Version,
		URL:       cfg.Update.URL,
		AutoCheck: cfg.Update.AutoCheck,
		Log:       lg.Logger.With("mod", "update"),
		OnChange:  hub.Notify,
	})
	go upd.AutoCheckLoop(ctx)

	srv = web.New(web.Deps{
		Log:         lg.Logger,
		Ring:        lg.Ring,
		Config:      cfg,
		Paths:       paths,
		Node:        node,
		Bridges:     bridges,
		Hub:         hub,
		ShowUI:      o.ShowUI,
		SetLogLevel: lg.SetLevel,
		Handoff:     o.Handoff,
		Updater:     upd,
		Restart:     o.Restart,
		Quit:        o.Quit,
		StartedAt:   time.Now(),
		Mode:        o.Mode,
	})

	if node.HasState() {
		lg.Info("发现已保存的节点状态，自动上线")
		if err := node.Start(ctx, "", nil); err != nil {
			lg.Error("自动上线失败", "err", err)
		}
	} else if key := os.Getenv("RSAGENT_AUTH_KEY"); key != "" {
		// 无人值守部署：环境变量给 key（和 tag），首次启动自动入网。key 不写日志。
		tags := SplitTags(os.Getenv("RSAGENT_TAGS"))
		lg.Info("用环境变量 RSAGENT_AUTH_KEY 自动登录", "tags", tags)
		if err := node.Start(ctx, key, tags); err != nil {
			lg.Error("自动登录失败", "err", err)
		}
	} else {
		lg.Info("节点未登录，等待在界面输入 auth key")
	}

	// 服务模式启动时，前一个实例可能还占着端口（例如刚从单进程切换过来），重试 30 秒。
	var ln net.Listener
	deadline := time.Now().Add(30 * time.Second)
	for {
		ln, err = net.Listen("tcp", cfg.Listen)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		lg.Warn("本机管理界面端口被占用，稍后重试", "addr", cfg.Listen, "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		bridges.StopAll()
		_ = node.Stop()
		return fmt.Errorf("监听 %s: %w", cfg.Listen, err)
	}
	lg.Info("本机管理界面已监听", "url", "http://"+cfg.Listen+"/")
	err = srv.Serve(ctx, ln, web.SourceLocal)
	bridges.StopAll()
	if stopErr := node.Stop(); stopErr != nil {
		lg.Warn("停止节点", "err", stopErr)
	}
	if err != nil {
		lg.Error("管理界面退出", "err", err)
		return err
	}
	lg.Info("rsagent 退出")
	return nil
}
