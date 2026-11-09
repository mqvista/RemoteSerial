package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"rsagent/internal/i18n"
	"strconv"
	"sync"

	"rsagent/internal/tsnode"
	"rsagent/internal/web"
)

// consoleNet 管理两个管理界面监听（本机/局域网、tailnet），支持运行中换端口：
// 先在新地址监听成功再关掉旧的；与自己的旧监听冲突时先关旧的再试，失败就恢复原样。
type consoleNet struct {
	ctx  context.Context
	srv  *web.Server
	node *tsnode.Node
	log  *slog.Logger
	wg   sync.WaitGroup

	mu      sync.Mutex
	local   *served
	bind    string
	tn      *served
	tnPort  int
	webPort int
	tnUp    bool // 节点已就绪（OnStarted 之后、OnStopped 之前）
}

type served struct {
	ln     net.Listener
	cancel context.CancelFunc
}

func (s *served) close() {
	if s != nil {
		s.cancel()
		_ = s.ln.Close()
	}
}

func (c *consoleNet) serve(ln net.Listener, src web.Source) *served {
	ctx, cancel := context.WithCancel(c.ctx)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if err := c.srv.Serve(ctx, ln, src); err != nil && !errors.Is(err, net.ErrClosed) {
			c.log.Debug(i18n.T("管理界面监听结束"), "source", src, "err", err)
		}
	}()
	return &served{ln: ln, cancel: cancel}
}

func portOf(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

func wildcard(addr string) bool {
	h, _, _ := net.SplitHostPort(addr)
	return h == "" || h == "0.0.0.0" || h == "::"
}

// startLocal 设置启动时已经监听好的本机监听。
func (c *consoleNet) startLocal(ln net.Listener, bind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.local = c.serve(ln, web.SourceLocal)
	c.bind = bind
}

// Apply 实现 web.ConsoleControl。
func (c *consoleNet) Apply(bind string, webPort int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bind != c.bind {
		ln, err := net.Listen("tcp", bind)
		if err != nil && (portOf(bind) == portOf(c.bind) || (c.tn != nil && portOf(bind) == c.tnPort)) {
			// 同一端口只是换绑定地址（例如打开局域网访问）：先关掉自己的旧监听
			oldBind := c.bind
			c.local.close()
			c.local = nil
			if c.tn != nil && portOf(bind) == c.tnPort {
				c.tn.close()
				c.tn = nil
			}
			ln, err = net.Listen("tcp", bind)
			if err != nil {
				if back, e := net.Listen("tcp", oldBind); e == nil {
					c.local = c.serve(back, web.SourceLocal)
				} else {
					c.log.Error(i18n.T("恢复原来的管理界面监听失败"), "addr", oldBind, "err", e)
				}
				c.syncTailnet()
				return err
			}
		}
		if err != nil {
			return err
		}
		old := c.local
		c.local = c.serve(ln, web.SourceLocal)
		c.bind = bind
		old.close()
		c.log.Info(i18n.T("本机管理界面已改为监听"), "addr", bind)
	}
	c.webPort = webPort
	c.syncTailnet()
	return nil
}

// syncTailnet 让 tailnet 侧监听与设置一致。系统 Tailscale 模式下本机监听若已绑定所有地址且端口相同，
// tailnet 的连接会直接进本机监听（按来源地址识别），不再另外监听。调用方持有 c.mu。
func (c *consoleNet) syncTailnet() {
	want := c.tnUp && !(c.node.System() && wildcard(c.bind) && portOf(c.bind) == c.webPort)
	if c.tn != nil && (!want || c.tnPort != c.webPort) {
		c.tn.close()
		c.tn = nil
	}
	if !want || c.tn != nil {
		return
	}
	addr := fmt.Sprintf(":%d", c.webPort)
	ln, err := c.node.Listen("tcp", addr)
	if err != nil {
		c.log.Error(i18n.T("tailnet 管理界面监听失败"), "addr", addr, "err", err)
		return
	}
	c.tn = c.serve(ln, web.SourceTailnet)
	c.tnPort = c.webPort
	c.log.Info(i18n.T("tailnet 管理界面已监听"), "port", c.webPort)
}

func (c *consoleNet) nodeStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tn.close() // 节点重启后旧监听已失效
	c.tn = nil
	c.tnUp = true
	c.syncTailnet()
}

func (c *consoleNet) nodeStopped() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tn.close()
	c.tn = nil
	c.tnUp = false
}

// wait 等所有监听在 ctx 取消后关闭完。
func (c *consoleNet) wait() { c.wg.Wait() }
