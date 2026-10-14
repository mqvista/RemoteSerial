// rsagent 是远程串口桥接 Agent 的入口。
//
// 子命令：
//
//	run      前台运行 core（tsnet 节点、串口桥接、HTTP 管理界面）
//	ui       托盘 + webview 壳（第 1 期）
//	service  Windows 服务 / systemd 相关操作（第 2 期）
//	deploy   提权执行部署动作（第 2 期）
//	version  打印版本信息
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"rsagent/internal/app"
	"rsagent/internal/buildinfo"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "version":
		fmt.Println(buildinfo.String())
	case "ui", "service", "deploy":
		err = fmt.Errorf("子命令 %q 尚未实现，见 docs/开发方案与计划.md 对应期数", os.Args[1])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `rsagent %s

用法:
  rsagent run [选项]     前台运行 core
  rsagent ui             托盘 + webview 壳（尚未实现）
  rsagent service ...    服务管理（尚未实现）
  rsagent deploy ...     部署动作（尚未实现）
  rsagent version        打印版本

run 选项:
  --data-dir <目录>      数据目录，默认见配置说明；也可用环境变量 RSAGENT_DATA_DIR
  --listen <地址>        本机管理界面监听地址，覆盖配置文件，默认 127.0.0.1:7890
  --log-level <级别>     debug | info | warn | error，覆盖配置文件
  --console              同时把日志输出到标准错误
`, buildinfo.Version)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var o app.Options
	fs.StringVar(&o.DataDir, "data-dir", os.Getenv("RSAGENT_DATA_DIR"), "数据目录")
	fs.StringVar(&o.Listen, "listen", "", "本机管理界面监听地址")
	fs.StringVar(&o.LogLevel, "log-level", "", "日志级别")
	fs.BoolVar(&o.Console, "console", false, "日志同时输出到标准错误")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, o)
}
