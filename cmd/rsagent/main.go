// rsagent 是远程串口桥接 Agent 的入口。
//
// 子命令：
//
//	ui       托盘 + webview 壳；本机没有 core 时在本进程启动 core（Windows 双击默认）
//	run      前台运行 core（tsnet 节点、串口桥接、HTTP 管理界面）
//	service  run | install | uninstall | start | stop | status
//	deploy   <action> | status，部署动作，需要时以管理员身份调用
//	version  打印版本信息
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"rsagent/internal/app"
	"rsagent/internal/buildinfo"
	"rsagent/internal/config"
	"rsagent/internal/deploy"
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
	case "service":
		err = cmdService(args[1:])
	case "deploy":
		err = cmdDeploy(args[1:])
	case "version":
		fmt.Println(buildinfo.String())
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
  rsagent ui [选项]              托盘 + 窗口；本机没有 core 时在本进程启动 core（Windows 无参数时默认）
  rsagent run [选项]             前台运行 core，不带界面
  rsagent service <op> [选项]    op: run | install | uninstall | start | stop | status
  rsagent deploy <action> [选项] action: %v | status
  rsagent version                打印版本

选项:
  --data-dir <目录>      数据目录，默认见配置说明；也可用环境变量 RSAGENT_DATA_DIR
  --listen <地址>        本机管理界面监听地址，覆盖配置文件，默认 127.0.0.1:7890
  --log-level <级别>     debug | info | warn | error，覆盖配置文件
  --console              日志同时输出到标准错误（仅 run）
`, buildinfo.Version, deploy.Actions)
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

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func cmdRun(args []string) error {
	o, err := parseCommon("run", args, true)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return app.Run(ctx, o)
}

func cmdUI(args []string) error {
	o, err := parseCommon("ui", args, false)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return app.RunUI(ctx, o)
}

// deployOptions 把命令行选项换算成部署动作需要的环境。
func deployOptions(o app.Options) (deploy.Options, error) {
	exe, err := os.Executable()
	if err != nil {
		return deploy.Options{}, err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	paths := config.ResolvePaths(o.DataDir)
	udp := 41641
	if cfg, _, err := config.Load(paths.ConfigFile); err == nil {
		udp = cfg.Tailscale.UDPPort
	}
	return deploy.Options{ExePath: exe, DataDir: paths.DataDir, Listen: o.Listen, UDPPort: udp}, nil
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: rsagent service run|install|uninstall|start|stop|status [选项]")
	}
	op := args[0]
	o, err := parseCommon("service "+op, args[1:], false)
	if err != nil {
		return err
	}
	dopts, err := deployOptions(o)
	if err != nil {
		return err
	}
	switch op {
	case "run":
		o.Mode = "service"
		return deploy.RunAsService(dopts, func(ctx context.Context) error { return app.Run(ctx, o) })
	case "install":
		return deploy.Run(dopts, deploy.ActionServiceInstall)
	case "uninstall", "remove":
		return deploy.Run(dopts, deploy.ActionServiceRemove)
	case "start":
		return deploy.Run(dopts, deploy.ActionServiceStart)
	case "stop":
		return deploy.Run(dopts, deploy.ActionServiceStop)
	case "status":
		st := deploy.GetStatus(dopts).Service
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	return fmt.Errorf("未知服务操作 %q", op)
}

// cmdDeploy 执行一个部署动作并把结果写回数据目录，供发起它的界面进程读取。
func cmdDeploy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: rsagent deploy <action>|status [选项]")
	}
	action := args[0]
	o, err := parseCommon("deploy "+action, args[1:], false)
	if err != nil {
		return err
	}
	dopts, err := deployOptions(o)
	if err != nil {
		return err
	}
	if action == "status" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(deploy.GetStatus(dopts))
	}
	res := deploy.Result{Action: action, OK: true, Message: "完成"}
	if err := deploy.Run(dopts, action); err != nil {
		res.OK = false
		res.Message = err.Error()
	}
	_ = deploy.WriteResult(dopts.DataDir, res)
	if !res.OK {
		return fmt.Errorf("%s: %s", action, res.Message)
	}
	fmt.Println(action, "完成")
	return nil
}
