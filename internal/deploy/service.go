package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/kardianos/service"
)

// ServiceName 是系统服务的内部名。
const ServiceName = "RemoteSerialAgent"

// ServiceConfig 返回 kardianos/service 的配置。
func ServiceConfig(o Options) *service.Config {
	args := []string{"service", "run", "--data-dir", o.DataDir}
	if o.Listen != "" {
		args = append(args, "--listen", o.Listen)
	}
	return &service.Config{
		Name:        ServiceName,
		DisplayName: "RemoteSerial Agent",
		Description: "远程串口桥接 Agent：内嵌 Tailscale 节点，把本机串口转成 tailnet 内可达的 TCP 服务。",
		Executable:  o.ExePath,
		Arguments:   args,
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
		return <-p.done
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
	st := ServiceStatus{Supported: true}
	svc, err := newControlService(o)
	if err != nil {
		st.Supported = false
		st.Detail = err.Error()
		return st
	}
	s, err := svc.Status()
	if err != nil {
		if strings.Contains(err.Error(), "not installed") || err == service.ErrNotInstalled {
			return st
		}
		st.Detail = err.Error()
		return st
	}
	st.Installed = true
	st.Running = s == service.StatusRunning
	return st
}

func serviceAction(o Options, action string) error {
	svc, err := newControlService(o)
	if err != nil {
		return err
	}
	switch action {
	case ActionServiceInstall:
		if err := svc.Install(); err != nil && !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("安装服务: %w", err)
		}
		if err := svc.Start(); err != nil {
			return fmt.Errorf("服务已安装但启动失败: %w", err)
		}
		return nil
	case ActionServiceRemove:
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil {
			return fmt.Errorf("卸载服务: %w", err)
		}
		return nil
	case ActionServiceStart:
		return svc.Start()
	case ActionServiceStop:
		return svc.Stop()
	}
	return fmt.Errorf("未知服务动作 %q", action)
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
