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
	"rsagent/internal/i18n"
	"rsagent/internal/platform"
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
	// 旧版 macOS 从 Finder 启动 .app 时会带上 -psn_ 进程序列号参数
	if len(args) > 0 && strings.HasPrefix(args[0], "-psn_") {
		args = args[1:]
	}
	if len(args) == 0 {
		if runtime.GOOS == "windows" || inMacAppBundle() {
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
	case "tailscale":
		err = cmdTailscale(args[1:])
	case "ctl":
		cmdCtl(args[1:])
	case "update":
		err = cmdUpdate(args[1:])
	case "quit":
		err = cmdQuit(args[1:])
	case "version":
		fmt.Println(buildinfo.String())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `rsagent %s

Usage:
  rsagent ui [options]              Tray/menu bar + window; starts core in-process if none is running
                                    (default when launched without arguments on Windows or from the .app)
  rsagent run [options]             Run core in the foreground without UI
  rsagent service <op> [options]    op: run | install | uninstall | start | stop | restart | status
  rsagent deploy <action> [options] action: %v | status
  rsagent provision [options]       --auth-key <key> [--tags tag:a,tag:b] [--hostname name] [--control-url url]
                                    Join the tailnet unattended; the key may also come from RSAGENT_AUTH_KEY
  rsagent tailscale <op> [options]  op: status | install | start | firewall | backend <embedded|system>
                                    Manage the system Tailscale (OpenWrt's tailscale package) used by rsagent
  rsagent ctl <op> [args]           JSON interface for LuCI: status | service | bridge | console | token |
                                    password | tailscale | tailscale-job | tailscale-login (run "rsagent ctl")
  rsagent update [options]          [--check] [--url manifest-url]  Check for, download and apply updates
  rsagent quit [options]            Ask the running tray instance on this machine to quit
  rsagent version                   Print the version

Options:
  --data-dir <dir>       Data directory (see config docs for the default); also RSAGENT_DATA_DIR
  --listen <addr>        Local admin UI listen address, overrides config, default 127.0.0.1:7890
  --log-level <level>    debug | info | warn | error, overrides config
  --console              Also write logs to stderr (run only)

Environment: RSAGENT_AUTH_KEY / RSAGENT_TAGS join the tailnet automatically on the first start of
run or service when not logged in. RSAGENT_LANG (en | zh-TW) sets the UI language when the config
does not specify one. Command-line output is always English.
`, buildinfo.Version, deploy.Actions)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func addCommon(fs *flag.FlagSet, o *app.Options, withConsole bool) {
	fs.StringVar(&o.DataDir, "data-dir", os.Getenv("RSAGENT_DATA_DIR"), "data directory")
	fs.StringVar(&o.Listen, "listen", "", "local admin UI listen address")
	fs.StringVar(&o.LogLevel, "log-level", "", "log level")
	if withConsole {
		fs.BoolVar(&o.Console, "console", false, "also write logs to stderr")
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
			return fmt.Errorf("restart failed: %w", rerr)
		}
	}
	return err
}

func cmdUI(args []string) error {
	// --background：只显示托盘/菜单栏图标，不弹出窗口（登录后启动时使用）
	background := false
	rest := args[:0:0]
	for _, a := range args {
		if a == "--background" {
			background = true
		} else {
			rest = append(rest, a)
		}
	}
	o, err := parseCommon("ui", rest, false)
	if err != nil {
		return err
	}
	o.Background = background
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
		return fmt.Errorf("usage: rsagent service run|install|uninstall|start|stop|restart|status [options]")
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
		return deploy.RunAsService(dopts, func(ctx context.Context) error {
			if runtime.GOOS != "darwin" && !platform.OpenWrt() {
				return app.Run(ctx, o)
			}
			// macOS 的服务以用户身份运行，没有权限让 launchd 重启自己；OpenWrt 上让 procd 重启自己
			// 又会连带结束发起重启的进程。两者都在更新后于本进程内停掉 core，再 exec 新程序，
			// PID 不变，服务管理器不会察觉。
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			var restart atomic.Bool
			o.Restart = func() { restart.Store(true); cancel() }
			err := app.Run(cctx, o)
			if restart.Load() && ctx.Err() == nil {
				if rerr := update.RestartSelf(); rerr != nil {
					return fmt.Errorf("restart failed: %w", rerr)
				}
			}
			return err
		})
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
	return fmt.Errorf("unknown service operation %q", op)
}

// cmdDeploy 执行一个部署动作并把结果写回数据目录，供发起它的界面进程读取。
func cmdDeploy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rsagent deploy <action>|status [options]")
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
	// 结果会显示在界面上，按配置的语言写；命令行输出仍是英文。
	i18n.SetLang(i18n.Resolve(config.PeekLanguage(config.ResolvePaths(o.DataDir).ConfigFile)))
	res := deploy.Result{Action: action, OK: true, Message: i18n.T("完成"), Time: time.Now()}
	runErr := deploy.Run(dopts, action)
	if runErr != nil {
		res.OK = false
		res.Message = runErr.Error()
	}
	_ = deploy.WriteResult(dopts.DataDir, res)
	i18n.SetLang(i18n.EN)
	if runErr != nil {
		return fmt.Errorf("%s: %v", action, runErr)
	}
	fmt.Println(action, "done")
	return nil
}

// cmdProvision 无人值守入网。
func cmdProvision(args []string) error {
	fs := newFlagSet("provision")
	var o app.Options
	addCommon(fs, &o, false)
	var (
		authKey    = fs.String("auth-key", "", "auth key or OAuth client secret; may also come from RSAGENT_AUTH_KEY")
		tags       = fs.String("tags", os.Getenv("RSAGENT_TAGS"), "tags to request when joining, comma-separated, e.g. tag:serial-agent")
		hostname   = fs.String("hostname", "", "node name; empty keeps the configured one")
		controlURL = fs.String("control-url", "", "control server URL; empty for the official one, or your Headscale URL")
		timeout    = fs.Duration("timeout", 2*time.Minute, "how long to wait for the node to come online")
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
		via = " (via the running core)"
	}
	fmt.Printf("Joined the tailnet%s: %s  IP %s  %s\n", via, res.Hostname, res.IP, res.DNSName)
	return nil
}

// cmdUpdate 命令行方式检查、下载并应用更新。
func cmdUpdate(args []string) error {
	fs := newFlagSet("update")
	var o app.Options
	addCommon(fs, &o, false)
	var (
		checkOnly = fs.Bool("check", false, "only check, do not download")
		urlFlag   = fs.String("url", "", "release manifest URL; empty uses the one in the config")
		force     = fs.Bool("force", false, "download and apply even if the version is not newer")
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
	fmt.Printf("Current version %s, latest %s (%s)\n", buildinfo.Version, st.Latest.Version, st.Latest.PublishedAt)
	if st.Latest.Notes != "" {
		fmt.Println(strings.TrimSpace(st.Latest.Notes))
	}
	if !st.Available {
		fmt.Println("Already up to date")
		return nil
	}
	if *checkOnly {
		fmt.Println("A new version is available; run without --check to download and apply it")
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
					return fmt.Errorf("download failed: %s", s.Error)
				}
				break
			}
			if s.Total > 0 {
				fmt.Printf("\rDownloading %d / %d KB", s.Received/1024, s.Total/1024)
			} else {
				fmt.Printf("\rDownloading %d KB", s.Received/1024)
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
	fmt.Printf("Downloaded %s, checksum verified\n", st.Downloaded.Version)

	dopts, err := deployOptions(o)
	if err != nil {
		return err
	}
	res := deploy.Execute(dopts, deploy.ActionUpdateApply)
	if !res.OK {
		return fmt.Errorf("applying the update failed: %s", res.Message)
	}
	listen := cfg.Listen
	if o.Listen != "" {
		listen = o.Listen
	}
	fmt.Printf("Updated to %s. ", st.Downloaded.Version)
	if svc := deploy.GetStatus(dopts).Service; svc.Installed && svc.Running {
		fmt.Println("The service has restarted on the new version.")
	} else if localCoreResponds(listen) {
		fmt.Println("The running instance is still the old version; restart it (quit the tray and run again, or rsagent quit then start it).")
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
		fmt.Println("No running instance on this machine")
		return nil
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the instance refused to quit: HTTP %d (in service mode use rsagent service stop)", resp.StatusCode)
	}
	// 等端口释放
	for i := 0; i < 50; i++ {
		if !localCoreResponds(listen) {
			fmt.Println("Quit")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("Quit request sent")
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

// inMacAppBundle 判断程序是否在 .app 包里运行（双击 .app 时没有参数，默认进入 ui）。
func inMacAppBundle() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}
