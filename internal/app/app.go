// Package app 把配置、日志、web 服务等组件装配起来，供各子命令复用。
//
// 第 0 期只有 HTTP 管理界面。第 1 期在此加入 tsnet 节点和串口桥接管理器。
package app

import (
	"context"
	"fmt"
	"time"

	"rsagent/internal/buildinfo"
	"rsagent/internal/config"
	"rsagent/internal/logging"
	"rsagent/internal/web"
)

// Options 是 run 子命令的运行参数，命令行覆盖配置文件。
type Options struct {
	DataDir  string
	Listen   string
	LogLevel string
	Console  bool
}

// Run 以单进程模式运行 core，阻塞到 ctx 取消。
func Run(ctx context.Context, o Options) error {
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

	srv := web.New(web.Deps{
		Log:       lg.Logger,
		Ring:      lg.Ring,
		Config:    cfg,
		Paths:     paths,
		StartedAt: time.Now(),
		Mode:      "standalone",
	})
	lg.Info("本机管理界面已监听", "url", "http://"+cfg.Listen+"/")

	err = srv.ListenAndServe(ctx, cfg.Listen, web.SourceLocal)
	if err != nil {
		lg.Error("管理界面退出", "err", err)
		return err
	}
	lg.Info("rsagent 退出")
	return nil
}
