// Package app 把配置、日志、tsnet 节点、web 服务等组件装配起来，供各子命令复用。
package app

import (
	"context"
	"net"
	"os"
	"rsagent/internal/i18n"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/buildinfo"
	"rsagent/internal/config"
	"rsagent/internal/logging"
	"rsagent/internal/platform"
	"rsagent/internal/tsnode"
	"rsagent/internal/update"
	"rsagent/internal/web"
)

// Options 是 run 子命令的运行参数，命令行覆盖配置文件。
type Options struct {
	DataDir    string
	Listen     string
	LogLevel   string
	Console    bool
	ShowUI     func() // 桌面模式下由壳层提供，用于单实例唤起窗口
	Background bool   // ui：启动时不弹出窗口，只显示托盘/菜单栏图标
	Mode       string // standalone | service，空为 standalone
	Handoff    func() // 部署页安装服务成功后调用，让本进程的 core 退位给服务
	Restart    func() // 应用更新后重启本进程；空表示不支持
	Quit       func() // 本机 POST /api/ui/quit 时退出本进程
}

// nodeConfig 由配置得到 Tailscale 节点参数。
func nodeConfig(cfg *config.Config, paths config.Paths) tsnode.Config {
	return tsnode.Config{
		StateDir:        paths.StateDir,
		Hostname:        cfg.Tailscale.Hostname,
		ControlURL:      cfg.Tailscale.ControlURL,
		UDPPort:         uint16(cfg.Tailscale.UDPPort),
		AdvertiseRoutes: cfg.Tailscale.AdvertiseRoutes,
		Tags:            cfg.Tailscale.Tags,
		Backend:         cfg.Tailscale.EffectiveBackend(),
		Socket:          cfg.Tailscale.Socket(),
	}
}

// Run 运行 core，阻塞到 ctx 取消。
func Run(ctx context.Context, o Options) error {
	if o.Mode == "" {
		o.Mode = "standalone"
	}
	paths := config.ResolvePaths(o.DataDir)
	if err := paths.Ensure(); err != nil {
		return i18n.Errorf("创建数据目录 %s: %w", paths.DataDir, err)
	}

	cfg, created, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	i18n.SetLang(i18n.Resolve(cfg.Language))
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

	lg.Info(i18n.T("rsagent 启动"), "version", buildinfo.Version, "commit", buildinfo.Commit, "dataDir", paths.DataDir,
		"platform", platform.Name(), "tailscale", cfg.Tailscale.EffectiveBackend())
	if created {
		lg.Info(i18n.T("已生成默认配置"), "file", paths.ConfigFile)
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

	con := &consoleNet{ctx: ctx, log: lg.Logger, webPort: cfg.Tailscale.WebPort}
	node = tsnode.New(nodeConfig(cfg, paths), lg.Logger.With("mod", "tsnode"), tsnode.Hooks{
		OnStarted: func(n *tsnode.Node) {
			con.nodeStarted()
			bridges.SetListener(n.Listen)
		},
		OnStopped: func() {
			con.nodeStopped()
			bridges.ClearListener()
		},
		OnChange: func(tsnode.Snapshot) { hub.Notify() },
	})
	con.node = node
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
		Console:     con,
	})
	con.srv = srv

	if node.HasState() {
		lg.Info(i18n.T("发现已保存的节点状态，自动上线"))
		if err := node.Start(ctx, "", nil); err != nil {
			lg.Error(i18n.T("自动上线失败"), "err", err)
		}
	} else if key := os.Getenv("RSAGENT_AUTH_KEY"); key != "" {
		// 无人值守部署：环境变量给 key（和 tag），首次启动自动入网。key 不写日志。
		tags := SplitTags(os.Getenv("RSAGENT_TAGS"))
		lg.Info(i18n.T("用环境变量 RSAGENT_AUTH_KEY 自动登录"), "tags", tags)
		if err := node.Start(ctx, key, tags); err != nil {
			lg.Error(i18n.T("自动登录失败"), "err", err)
		} else if len(tags) > 0 {
			cfg.Tailscale.Tags = tags
			if err := cfg.Save(paths.ConfigFile); err != nil {
				lg.Error(i18n.T("保存配置失败"), "err", err)
			}
		}
	} else {
		lg.Info(i18n.T("节点未登录，等待在界面输入 auth key"))
	}

	// 服务模式启动时，前一个实例可能还占着端口（例如刚从单进程切换过来），重试 30 秒。
	bind := cfg.ConsoleBind()
	var ln net.Listener
	deadline := time.Now().Add(30 * time.Second)
	for {
		ln, err = net.Listen("tcp", bind)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		lg.Warn(i18n.T("本机管理界面端口被占用，稍后重试"), "addr", bind, "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		bridges.StopAll()
		_ = node.Stop()
		return i18n.Errorf("监听 %s: %w", bind, err)
	}
	lg.Info(i18n.T("本机管理界面已监听"), "url", "http://"+cfg.Listen+"/", "lan", cfg.Access.LAN)
	con.startLocal(ln, bind)
	<-ctx.Done()
	con.wait()
	bridges.StopAll()
	if stopErr := node.Stop(); stopErr != nil {
		lg.Warn(i18n.T("停止节点"), "err", stopErr)
	}
	lg.Info(i18n.T("rsagent 退出"))
	return nil
}
