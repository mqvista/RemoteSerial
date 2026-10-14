// rsagent 是远程串口桥接 Agent 的入口。
//
// 子命令：
//
//	ui       托盘 + webview 壳；本机没有 core 时在本进程启动 core（Windows 双击默认）
//	run      前台运行 core（tsnet 节点、串口桥接、HTTP 管理界面）
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
	"runtime"
	"syscall"

	"rsagent/internal/app"
	"rsagent/internal/buildinfo"
	"rsagent/internal/shell"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		if runtime.GOOS == "windows" {
			args = []string{"ui"}
		} else {
			usage()
			os.Exit(2)
		}
	}
	// 发布版以 windowsgui 子系统构建，没有控制台；在终端里执行非 ui 子命令时接回父进程的控制台。
	if args[0] != "ui" {
		shell.AttachParentConsole()
	}

	var err error
	switch args[0] {
	case "ui":
		err = cmdUI(args[1:])
	case "run":
		err = cmdRun(args[1:])
	case "version":
		fmt.Println(buildinfo.String())
	case "service", "deploy":
		err = fmt.Errorf("子命令 %q 尚未实现，见 docs/开发方案与计划.md 第 2 期", args[0])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", args[0])
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
  rsagent ui [选项]      托盘 + 窗口；本机没有 core 时在本进程启动 core（Windows 无参数时默认）
  rsagent run [选项]     前台运行 core，不带界面
  rsagent service ...    服务管理（尚未实现）
  rsagent deploy ...     部署动作（尚未实现）
  rsagent version        打印版本

选项:
  --data-dir <目录>      数据目录，默认见配置说明；也可用环境变量 RSAGENT_DATA_DIR
  --listen <地址>        本机管理界面监听地址，覆盖配置文件，默认 127.0.0.1:7890
  --log-level <级别>     debug | info | warn | error，覆盖配置文件
  --console              日志同时输出到标准错误（仅 run）
`, buildinfo.Version)
}

func parseCommon(name string, args []string, withConsole bool) (app.Options, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var o app.Options
	fs.StringVar(&o.DataDir, "data-dir", os.Getenv("RSAGENT_DATA_DIR"), "数据目录")
	fs.StringVar(&o.Listen, "listen", "", "本机管理界面监听地址")
	fs.StringVar(&o.LogLevel, "log-level", "", "日志级别")
	if withConsole {
		fs.BoolVar(&o.Console, "console", false, "日志同时输出到标准错误")
	}
	err := fs.Parse(args)
	return o, err
}

func cmdRun(args []string) error {
	o, err := parseCommon("run", args, true)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, o)
}

func cmdUI(args []string) error {
	o, err := parseCommon("ui", args, false)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.RunUI(ctx, o)
}
