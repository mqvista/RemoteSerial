// rsagent 是远程串口桥接 Agent 的入口。
//
// 子命令：
//
//	ui         托盘 + webview 壳；本机没有 core 时在本进程启动 core（Windows 双击默认）
//	run        前台运行 core（tsnet 节点、串口桥接、HTTP 管理界面）
//	service    run | install | uninstall | start | stop | restart | status
//	deploy     <action> | status，部署动作，需要时以管理员身份调用
//	provision  用 auth key 或 OAuth client secret 无人值守入网，供安装程序与批量部署脚本使用
//	update     检查、下载并应用更新（命令行方式，适合 Linux 与无界面站点）
//	quit       让本机运行中的托盘实例退出
//	version    打印版本信息
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"rsagent/internal/app"
	"rsagent/internal/buildinfo"
	"rsagent/internal/config"
	"rsagent/internal/deploy"
	"rsagent/internal/shell"
	"rsagent/internal/update"
)

func main() {
	// 由更新流程拉起时，先等旧进程退出，端口和串口才会空出来。
	if v := os.Getenv("RSAGENT_WAIT_PID"); v != "" {
		_ = os.Unsetenv("RSAGENT_WAIT_PID")
		if pid, err := strconv.Atoi(v); err == nil && pid != os.Getpid() {
			update.WaitForExit(pid, 30*time.Second)
		}
	}
	if exe, err := os.Executable(); err == nil {
		update.CleanupOld(exe)
	}

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
	case "provision":
		err = cmdProvision(args[1:])
	case "update":
		err = cmdUpdate(args[1:])
	case "quit":
		err = cmdQuit(args[1:])
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
  rsagent service <op> [选项]    op: run | install | uninstall | start | stop | restart | status
  rsagent deploy <action> [选项] action: %v | status
  rsagent provision [选项]       --auth-key <key> [--tags tag:a,tag:b] [--hostname 名] [--control-url 地址]
                                 无人值守入网；key 也可用环境变量 RSAGENT_AUTH_KEY 传入
  rsagent update [选项]          [--check] [--url 清单地址]  检查、下载并应用更新
  rsagent quit [选项]            让本机运行中的托盘实例退出
  rsagent version                打印版本

选项:
  --data-dir <目录>      数据目录，默认见配置说明；也可用环境变量 RSAGENT_DATA_DIR
  --listen <地址>        本机管理界面监听地址，覆盖配置文件，默认 127.0.0.1:7890
  --log-level <级别>     debug | info | warn | error，覆盖配置文件
  --console              日志同时输出到标准错误（仅 run）

环境变量 RSAGENT_AUTH_KEY / RSAGENT_TAGS：run 或 service 首次启动且未登录时自动入网。
`, buildinfo.Version, deploy.Actions)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func addCommon(fs *flag.FlagSet, o *app.Options, withConsole bool) {
	fs.StringVar(&o.DataDir, "data-dir", os.Getenv("RSAGENT_DATA_DIR"), "数据目录")
	fs.StringVar(&o.Listen, "listen", "", "本机管理界面监听地址")
	fs.StringVar(&o.LogLevel, "log-level", "", "日志级别")
	if withConsole {
		fs.BoolVar(&o.Console, "console", false, "日志同时输出到标准错误")
	}
}

func parseCommon(name string, args []string, withConsole bool) (app.Options, error) {
	fs := newFlagSet(name)
	var o app.Options
	addCommon(fs, &o, withConsole)
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
	update.CleanupApplied(config.ResolvePaths(o.DataDir).DataDir, buildinfo.Version)
	var restart atomic.Bool
	o.Restart = func() { restart.Store(true); stop() }
	o.Quit = stop
	err = app.Run(ctx, o)
	if restart.Load() {
		// Linux 上 exec 原地替换，PID 不变；Windows 上拉起新进程后本进程退出。
		if rerr := update.RestartSelf(); rerr != nil {
			return fmt.Errorf("重启失败: %w", rerr)
		}
	}
	return err
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
		return fmt.Errorf("用法: rsagent service run|install|uninstall|start|stop|restart|status [选项]")
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
	case "restart":
		return deploy.Run(dopts, deploy.ActionServiceRestart)
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
	res := deploy.Result{Action: action, OK: true, Message: "完成", Time: time.Now()}
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

// cmdProvision 无人值守入网。
func cmdProvision(args []string) error {
	fs := newFlagSet("provision")
	var o app.Options
	addCommon(fs, &o, false)
	var (
		authKey    = fs.String("auth-key", "", "auth key 或 OAuth client secret；也可用环境变量 RSAGENT_AUTH_KEY")
		tags       = fs.String("tags", os.Getenv("RSAGENT_TAGS"), "入网申请的 tag，逗号分隔，如 tag:serial-agent")
		hostname   = fs.String("hostname", "", "节点名，空则沿用配置")
		controlURL = fs.String("control-url", "", "控制服务器地址，空为官方；Headscale 填其地址")
		timeout    = fs.Duration("timeout", 2*time.Minute, "等待上线的时间")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := *authKey
	if key == "" {
		key = os.Getenv("RSAGENT_AUTH_KEY")
	}
	controlSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "control-url" {
			controlSet = true
		}
	})
	ctx, stop := signalContext()
	defer stop()
	res, err := app.Provision(ctx, app.ProvisionOptions{
		DataDir:       o.DataDir,
		Listen:        o.Listen,
		AuthKey:       key,
		Tags:          app.SplitTags(*tags),
		Hostname:      *hostname,
		ControlURL:    *controlURL,
		ControlURLSet: controlSet,
		Timeout:       *timeout,
	})
	if err != nil {
		return err
	}
	via := ""
	if res.ViaCore {
		via = "（由运行中的 core 完成）"
	}
	fmt.Printf("已入网%s：%s  IP %s  %s\n", via, res.Hostname, res.IP, res.DNSName)
	return nil
}

// cmdUpdate 命令行方式检查、下载并应用更新。
func cmdUpdate(args []string) error {
	fs := newFlagSet("update")
	var o app.Options
	addCommon(fs, &o, false)
	var (
		checkOnly = fs.Bool("check", false, "只检查，不下载")
		urlFlag   = fs.String("url", "", "发布清单地址，空则用配置文件中的地址")
		force     = fs.Bool("force", false, "版本不比当前新也下载并应用")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths := config.ResolvePaths(o.DataDir)
	if err := paths.Ensure(); err != nil {
		return err
	}
	cfg, _, err := config.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	url := cfg.Update.URL
	if *urlFlag != "" {
		url = *urlFlag
	}
	ctx, stop := signalContext()
	defer stop()

	current := buildinfo.Version
	if *force {
		// 强制模式：把当前版本当作最旧，任何清单版本都视为可更新
		current = "0.0.0"
	}
	u := update.New(update.Options{DataDir: paths.DataDir, Current: current, URL: url})
	st, err := u.Check(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("当前版本 %s，最新版本 %s（%s）\n", buildinfo.Version, st.Latest.Version, st.Latest.PublishedAt)
	if st.Latest.Notes != "" {
		fmt.Println(strings.TrimSpace(st.Latest.Notes))
	}
	if !st.Available {
		fmt.Println("已是最新版本")
		return nil
	}
	if *checkOnly {
		fmt.Println("有新版本可用，去掉 --check 以下载并应用")
		return nil
	}
	if st.Downloaded == nil || st.Downloaded.Version != st.Latest.Version {
		if err := u.StartDownload(ctx); err != nil {
			return err
		}
		for {
			s := u.Status()
			if !s.Downloading {
				if s.Error != "" {
					return fmt.Errorf("下载失败: %s", s.Error)
				}
				break
			}
			if s.Total > 0 {
				fmt.Printf("\r下载中 %d / %d KB", s.Received/1024, s.Total/1024)
			} else {
				fmt.Printf("\r下载中 %d KB", s.Received/1024)
			}
			select {
			case <-ctx.Done():
				u.CancelDownload()
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		fmt.Println()
	}
	st = u.Status()
	fmt.Printf("已下载 %s，校验通过\n", st.Downloaded.Version)

	dopts, err := deployOptions(o)
	if err != nil {
		return err
	}
	res := deploy.Execute(dopts, deploy.ActionUpdateApply)
	if !res.OK {
		return fmt.Errorf("应用更新失败: %s", res.Message)
	}
	listen := cfg.Listen
	if o.Listen != "" {
		listen = o.Listen
	}
	fmt.Printf("已更新到 %s。", st.Downloaded.Version)
	if svc := deploy.GetStatus(dopts).Service; svc.Installed && svc.Running {
		fmt.Println("服务已重启到新版本。")
	} else if localCoreResponds(listen) {
		fmt.Println("运行中的实例仍是旧版本，请重启它（托盘退出后重新运行，或 rsagent quit 后重新启动）。")
	} else {
		fmt.Println()
	}
	return nil
}

// cmdQuit 让本机运行中的实例退出。
func cmdQuit(args []string) error {
	o, err := parseCommon("quit", args, false)
	if err != nil {
		return err
	}
	paths := config.ResolvePaths(o.DataDir)
	listen := "127.0.0.1:7890"
	if cfg, _, err := config.Load(paths.ConfigFile); err == nil {
		listen = cfg.Listen
	}
	if o.Listen != "" {
		listen = o.Listen
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post("http://"+listen+"/api/ui/quit", "application/json", nil)
	if err != nil {
		fmt.Println("本机没有运行中的实例")
		return nil
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("实例拒绝退出: HTTP %d（服务模式请用 rsagent service stop）", resp.StatusCode)
	}
	// 等端口释放
	for i := 0; i < 50; i++ {
		if !localCoreResponds(listen) {
			fmt.Println("已退出")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("已发送退出请求")
	return nil
}

func localCoreResponds(listen string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + listen + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
