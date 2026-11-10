package app

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/i18n"
	"rsagent/internal/netproxy"
	"rsagent/internal/platform"
	"rsagent/internal/systs"
	"rsagent/internal/tsnode"
)

// proxyCtl 套用前置代理设置（实现 web.ProxyControl）。
type proxyCtl struct {
	ctx  context.Context
	log  *slog.Logger
	node *tsnode.Node

	mu      sync.Mutex
	want    *url.URL // 系统 tailscaled 应使用的代理，nil 表示不用
	sysErr  string
	syncing sync.Mutex
}

func newProxyCtl(ctx context.Context, log *slog.Logger, node *tsnode.Node, p config.Proxy) *proxyCtl {
	c := &proxyCtl{ctx: ctx, log: log, node: node}
	netproxy.Set(p)
	c.want = tailscaleProxy(p)
	return c
}

// tailscaleProxy 返回 Tailscale 应使用的代理地址。
func tailscaleProxy(p config.Proxy) *url.URL {
	if !p.Tailscale || p.URL == "" {
		return nil
	}
	u, err := config.ParseProxyURL(p.URL)
	if err != nil {
		return nil
	}
	return u
}

func sameURL(a, b *url.URL) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.String() == b.String()
}

// manageSystem 报告是否由 rsagent 修改系统 tailscaled 的服务脚本。
func (c *proxyCtl) manageSystem() bool {
	return c.node.System() && platform.OpenWrt()
}

// Apply 套用新设置（不等待）。Tailscale 代理有变化时：系统 tailscaled 改服务脚本并重启；内嵌节点在运行中则重新连线。
func (c *proxyCtl) Apply(old, p config.Proxy) {
	netproxy.Set(p)
	want := tailscaleProxy(p)
	c.mu.Lock()
	c.want = want
	c.mu.Unlock()
	if sameURL(tailscaleProxy(old), want) {
		return
	}
	// 稍后再套用：会重启或重新连线 Tailscale，这次请求可能正是经 tailnet 来的，先让回应送出去
	if c.manageSystem() {
		go func() {
			if c.sleep(time.Second) {
				_ = c.syncSystem()
			}
		}()
		return
	}
	if c.node.Running() {
		go func() {
			if !c.sleep(time.Second) {
				return
			}
			c.log.Info(i18n.T("代理设置已变更，重新连线 Tailscale"))
			if err := c.node.Stop(); err != nil {
				c.log.Warn(i18n.T("停止节点"), "err", err)
			}
			if err := c.node.Start(c.ctx, "", nil); err != nil {
				c.log.Error(i18n.T("重新连线 Tailscale 失败"), "err", err)
			}
		}()
	}
}

func (c *proxyCtl) sleep(d time.Duration) bool {
	select {
	case <-c.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// syncSystem 让系统 tailscaled 的服务脚本与设置一致。
func (c *proxyCtl) syncSystem() error {
	c.syncing.Lock()
	defer c.syncing.Unlock()
	c.mu.Lock()
	want := c.want
	c.mu.Unlock()
	changed, err := systs.SyncProxy(c.ctx, io.Discard, want)
	c.mu.Lock()
	if err != nil {
		c.sysErr = err.Error()
	} else {
		c.sysErr = ""
	}
	c.mu.Unlock()
	if err != nil {
		c.log.Warn(i18n.T("设置系统 tailscale 的代理失败"), "err", err)
		return err
	}
	if changed {
		if want != nil {
			c.log.Info(i18n.T("系统 tailscale 已改用代理"), "proxy", netproxy.Redact(want.String()))
		} else {
			c.log.Info(i18n.T("系统 tailscale 已停用代理"))
		}
		c.node.Poke()
	}
	return nil
}

// SystemState 报告系统 tailscaled 的代理设置是否已写入服务脚本，以及最近一次的错误。
func (c *proxyCtl) SystemState() (managed, hooked bool, errText string) {
	if !c.manageSystem() {
		return false, false, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return true, systs.ProxyHooked(), c.sysErr
}

// loop 定期检查系统 tailscaled 的服务脚本：tailscale 套件升级会覆盖它，代理设置需要补回。
func (c *proxyCtl) loop() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		if c.manageSystem() {
			_ = c.syncSystem()
		}
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
	}
}
