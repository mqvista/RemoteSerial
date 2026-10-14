package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
)

// ServiceName 是系统服务的内部名。
const ServiceName = "RemoteSerialAgent"

// ServiceBinary 返回服务使用的程序副本路径。
//
// 服务账户看不到用户的映射盘，也不该依赖用户随手放的位置，所以安装时把程序
// 复制到数据目录下的 bin\ 并用副本注册。更新程序后重新点一次"安装"即可。
func ServiceBinary(o Options) string {
	name := "rsagent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(o.DataDir, "bin", name)
}

func serviceArgs(o Options) []string {
	args := []string{"service", "run", "--data-dir", o.DataDir}
	if o.Listen != "" {
		args = append(args, "--listen", o.Listen)
	}
	return args
}

// ServiceConfig 返回 kardianos/service 的配置。
func ServiceConfig(o Options) *service.Config {
	return &service.Config{
		Name:        ServiceName,
		DisplayName: "RemoteSerial Agent",
		Description: "远程串口桥接 Agent：内嵌 Tailscale 节点，把本机串口转成 tailnet 内可达的 TCP 服务。",
		Executable:  ServiceBinary(o),
		Arguments:   serviceArgs(o),
		Option: service.KeyValue{
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   10,
			"Restart":                "always", // systemd
			"RestartSec":             5,
		},
	}
}

// Program 是 kardianos/service 的回调实现，Run 函数由调用方注入（避免 deploy 依赖 app）。
type Program struct {
	RunCore func(ctx context.Context) error
	cancel  context.CancelFunc
	done    chan error
}

// Start 由服务管理器调用，非阻塞。
func (p *Program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	go func() { p.done <- p.RunCore(ctx) }()
	return nil
}

// Stop 由服务管理器调用。
func (p *Program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		select {
		case err := <-p.done:
			return err
		case <-time.After(20 * time.Second):
			return errors.New("core 停止超时")
		}
	}
	return nil
}

type noopProgram struct{}

func (noopProgram) Start(service.Service) error { return nil }
func (noopProgram) Stop(service.Service) error  { return nil }

func newControlService(o Options) (service.Service, error) {
	return service.New(noopProgram{}, ServiceConfig(o))
}

func serviceStatus(o Options) ServiceStatus {
	st := ServiceStatus{Supported: true, Binary: ServiceBinary(o)}
	installed, running, err := queryServiceStatus(o)
	if err != nil {
		st.Detail = err.Error()
	}
	st.Installed = installed
	st.Running = running
	return st
}

// queryServiceStatusGeneric 用 kardianos/service 查询，非 Windows 平台使用。
func queryServiceStatusGeneric(o Options) (installed, running bool, err error) {
	svc, err := newControlService(o)
	if err != nil {
		return false, false, err
	}
	s, err := svc.Status()
	if err != nil {
		if errors.Is(err, service.ErrNotInstalled) || strings.Contains(err.Error(), "not installed") {
			return false, false, nil
		}
		return false, false, err
	}
	return true, s == service.StatusRunning, nil
}

func serviceAction(o Options, action string) error {
	svc, err := newControlService(o)
	if err != nil {
		return err
	}
	switch action {
	case ActionServiceInstall:
		return serviceInstall(o, svc)
	case ActionServiceRemove:
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil && !errors.Is(err, service.ErrNotInstalled) {
			return fmt.Errorf("卸载服务: %w", err)
		}
		return nil
	case ActionServiceStart:
		if err := svc.Start(); err != nil {
			return fmt.Errorf("启动服务: %w", err)
		}
		return nil
	case ActionServiceStop:
		if err := svc.Stop(); err != nil {
			return fmt.Errorf("停止服务: %w", err)
		}
		return nil
	}
	return fmt.Errorf("未知服务动作 %q", action)
}

func serviceInstall(o Options, svc service.Service) error {
	installed := serviceStatus(o).Installed
	if installed {
		_ = svc.Stop()
	}
	if err := copyBinary(o.ExePath, ServiceBinary(o)); err != nil {
		return fmt.Errorf("复制程序到 %s: %w", ServiceBinary(o), err)
	}
	if installed {
		if err := updateServiceBinPath(o); err != nil {
			return fmt.Errorf("更新服务路径: %w", err)
		}
	} else {
		if err := svc.Install(); err != nil {
			return fmt.Errorf("安装服务: %w", err)
		}
	}
	if err := svc.Start(); err != nil {
		return fmt.Errorf("服务已安装但启动失败: %w。可查看数据目录 logs\\rsagent.log", err)
	}
	return nil
}

// copyBinary 把当前程序复制到服务路径。目标被占用时重试几次。
func copyBinary(src, dst string) error {
	if sameFile(src, dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var lastErr error
	for i := 0; i < 5; i++ {
		lastErr = copyFile(src, dst)
		if lastErr == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}

// RunAsService 在服务管理器控制下运行 core。
func RunAsService(o Options, runCore func(ctx context.Context) error) error {
	prg := &Program{RunCore: runCore}
	svc, err := service.New(prg, ServiceConfig(o))
	if err != nil {
		return err
	}
	return svc.Run()
}
