package tsnode

// system 后端：不内嵌 tsnet，改为通过 LocalAPI 使用系统自带的 tailscaled（OpenWrt 的 tailscale 套件）。
//
// 与内嵌节点共用状态映射（watch / applyState）、WhoIs、Peers、Ping、路由查询，区别在于：
//   - 生命周期由 sysLoop 管：检测是否安装、守护进程是否在跑、socket 能否连上，连上后订阅 IPN 总线，
//     断开（tailscaled 重启或被卸载）后回到检测。
//   - 监听用普通 net.Listen 绑定本机的 tailscale IP；节点 Running 且拿到 IP 时调用 OnStarted，
//     离开 Running 或 IP 变化时调用 OnStopped 并关掉这些监听。
//   - 不提供登出、停止守护进程、netcheck；子网路由只增删 rsagent 自己加的条目。

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"

	"rsagent/internal/i18n"
	"rsagent/internal/systs"
)

const sysRetryInterval = 5 * time.Second

// System 判断是否使用系统 tailscaled。
func (n *Node) System() bool { return n.cfg.Backend == BackendSystem }

// Backend 返回网络后端名。
func (n *Node) Backend() string {
	if n.System() {
		return BackendSystem
	}
	return BackendEmbedded
}

// 网络后端名，与配置文件中的取值一致。
const (
	BackendEmbedded = "embedded"
	BackendSystem   = "system"
)

// Poke 让 system 后端立即重新检测（安装或启动 tailscale 之后调用）。
func (n *Node) Poke() {
	select {
	case n.sysPoke <- struct{}{}:
	default:
	}
}

// RefreshSystem 立即重新检测系统 tailscale 的安装与防火墙状态，不影响连接。
func (n *Node) RefreshSystem() {
	if !n.System() {
		return
	}
	info := systs.Detect(n.cfg.Socket)
	n.mu.Lock()
	info.SocketOK = n.sys.SocketOK
	info.Version = n.sys.Version
	if info.SocketOK {
		info.Installed = true
	}
	n.sys = info
	n.mu.Unlock()
	n.emit()
	n.Poke()
}

func (n *Node) sysStart(ctx context.Context, authKey string, tags []string) error {
	n.mu.Lock()
	if len(tags) > 0 {
		n.cfg.Tags = append([]string(nil), tags...)
		n.snap.AdvertiseTags = append([]string(nil), tags...)
	} else {
		tags = n.cfg.Tags
	}
	if n.sysCancel == nil {
		lctx, cancel := context.WithCancel(context.Background())
		n.sysCancel = cancel
		n.sysDone = make(chan struct{})
		n.setStateLocked(StateStarting, nil)
		go n.sysLoop(lctx, n.sysDone)
	}
	n.mu.Unlock()
	n.emit()
	if authKey == "" {
		return nil
	}
	lc, err := n.waitLocalClient(ctx, 15*time.Second)
	if err != nil {
		return err
	}
	return n.sysLogin(ctx, lc, authKey, tags)
}

// waitLocalClient 等 sysLoop 连上 tailscaled。
func (n *Node) waitLocalClient(ctx context.Context, d time.Duration) (*local.Client, error) {
	deadline := time.Now().Add(d)
	for {
		n.mu.Lock()
		lc := n.lc
		st := n.snap.State
		n.mu.Unlock()
		if lc != nil {
			return lc, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if st == StateNotInstalled {
				return nil, i18n.New("系统未安装 Tailscale")
			}
			return nil, i18n.New("无法连接系统 Tailscale（tailscaled 未运行？）")
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// sysLogin 用 auth key 让系统 tailscaled 登录，与 tailscale up --auth-key 相同：
// 带上 tag 与控制服务器地址启动，没有节点密钥或需要重新认证时再发起登录。
func (n *Node) sysLogin(ctx context.Context, lc *local.Client, authKey string, tags []string) error {
	key, err := resolveOAuthKey(ctx, authKey, tags)
	if err != nil {
		return err
	}
	cur, err := lc.GetPrefs(ctx)
	if err != nil {
		return i18n.Errorf("读取系统 Tailscale 设置: %w", err)
	}
	p := cur.Clone()
	p.WantRunning = true
	if len(tags) > 0 {
		p.AdvertiseTags = append([]string(nil), tags...)
	}
	if u := n.Config().ControlURL; u != "" {
		p.ControlURL = u
	}
	if err := lc.Start(ctx, ipn.Options{AuthKey: key, UpdatePrefs: p}); err != nil {
		return i18n.Errorf("系统 Tailscale 登录失败: %w", err)
	}
	st, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		return err
	}
	if !st.HaveNodeKey || st.BackendState == ipn.NeedsLogin.String() {
		if err := lc.StartLoginInteractive(ctx); err != nil {
			return i18n.Errorf("系统 Tailscale 登录失败: %w", err)
		}
	}
	n.log.Info(i18n.T("已用 auth key 发起系统 Tailscale 登录"), "tags", tags)
	return nil
}

// LoginInteractive 发起浏览器登录：认证链接经 IPN 总线到达后出现在 Snapshot.AuthURL。
func (n *Node) LoginInteractive(ctx context.Context) error {
	n.mu.Lock()
	lc := n.lc
	n.mu.Unlock()
	if lc == nil {
		return i18n.New("节点未启动")
	}
	if n.System() {
		if _, err := lc.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: true}, WantRunningSet: true}); err != nil {
			return err
		}
	}
	return lc.StartLoginInteractive(ctx)
}

func (n *Node) sysStop() error {
	n.mu.Lock()
	cancel, done := n.sysCancel, n.sysDone
	n.sysCancel, n.sysDone = nil, nil
	n.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	<-done
	n.sysDetach()
	n.mu.Lock()
	n.lc = nil
	n.snap.AuthURL = ""
	n.setStateLocked(StateStopped, nil)
	n.mu.Unlock()
	n.emit()
	return nil
}

// sysLoop 检测并连接系统 tailscaled，断开后重新检测，直到 ctx 取消。
func (n *Node) sysLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for ctx.Err() == nil {
		info := systs.Detect(n.cfg.Socket)
		lc := &local.Client{Socket: n.cfg.Socket, UseSocketOnly: true}
		_, sockErr := os.Stat(n.cfg.Socket)
		var err error = sockErr
		if sockErr == nil {
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			st, serr := lc.StatusWithoutPeers(cctx)
			cancel()
			err = serr
			if serr == nil {
				info.SocketOK, info.Installed, info.Version = true, true, st.Version
			}
		}
		if info.SocketOK {
			n.mu.Lock()
			n.sys = info
			n.lc = lc
			n.mu.Unlock()
			n.log.Info(i18n.T("已连接系统 Tailscale"), "socket", n.cfg.Socket, "version", info.Version)
			n.emit()
			n.watchOnce(ctx, lc)
			n.mu.Lock()
			n.lc = nil
			n.mu.Unlock()
			n.sysDetach()
			if ctx.Err() != nil {
				return
			}
			n.log.Warn(i18n.T("与系统 Tailscale 的连接断开，重新检测"))
			continue
		}

		var s State
		var msg i18n.Text
		switch {
		case !info.Installed && errors.Is(sockErr, os.ErrNotExist):
			s, msg = StateNotInstalled, i18n.M("系统未安装 Tailscale")
		case !info.DaemonRunning || errors.Is(sockErr, os.ErrNotExist):
			s, msg = StateNotRunning, i18n.M("系统 Tailscale 未运行")
		default:
			s, msg = StateError, i18n.ErrText(i18n.Errorf("连接系统 Tailscale 失败: %w", err))
		}
		n.mu.Lock()
		changed := n.snap.State != s
		n.sys = info
		n.snap.IP, n.snap.IPv6, n.snap.AuthURL = "", "", ""
		n.setStateLocked(s, msg)
		n.mu.Unlock()
		if changed {
			n.log.Warn(i18n.T("系统 Tailscale 不可用"), "state", s, "socket", n.cfg.Socket)
		}
		n.emit()
		select {
		case <-ctx.Done():
			return
		case <-n.sysPoke:
		case <-time.After(sysRetryInterval):
		}
	}
}

// sysAttach 在节点 Running 且有 IP 时让 app 创建 tailnet 侧监听；IP 变了就先拆掉旧的。
func (n *Node) sysAttach() {
	n.mu.Lock()
	ip := n.snap.IP
	same := n.attached && n.attachedIP == ip
	was := n.attached
	n.mu.Unlock()
	if ip == "" || same {
		return
	}
	if was {
		n.sysDetach()
	}
	n.mu.Lock()
	n.attached, n.attachedIP = true, ip
	n.mu.Unlock()
	n.log.Info(i18n.T("在 tailscale 地址上开始监听"), "ip", ip)
	if n.hooks.OnStarted != nil {
		n.hooks.OnStarted(n)
	}
}

// sysDetach 撤销 sysAttach：先通知 app（停止桥接），再关闭剩余监听（tailnet 侧管理界面）。
func (n *Node) sysDetach() {
	n.mu.Lock()
	if !n.attached {
		n.mu.Unlock()
		return
	}
	n.attached, n.attachedIP = false, ""
	lns := n.sysLns
	n.sysLns = nil
	n.mu.Unlock()
	if n.hooks.OnStopped != nil {
		n.hooks.OnStopped()
	}
	for ln := range lns {
		_ = ln.Close()
	}
}

// sysListen 在本机 tailscale IP 上监听；addr 形如 ":2217"。
func (n *Node) sysListen(network, addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.attached {
		return nil, i18n.New("节点未启动")
	}
	if host == "" {
		host = n.attachedIP
	}
	ln, err := net.Listen(network, net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	tl := &trackedListener{Listener: ln, n: n}
	if n.sysLns == nil {
		n.sysLns = map[*trackedListener]struct{}{}
	}
	n.sysLns[tl] = struct{}{}
	return tl, nil
}

// trackedListener 关闭时从 Node 的列表里移除自己。
type trackedListener struct {
	net.Listener
	n    *Node
	once sync.Once
}

func (l *trackedListener) Close() error {
	l.once.Do(func() {
		l.n.mu.Lock()
		delete(l.n.sysLns, l)
		l.n.mu.Unlock()
	})
	return l.Listener.Close()
}

// mergeRoutes 修改系统 tailscaled 宣告的子网路由：去掉 old 中不再需要的，加上 routes 中缺的，
// 其余（用户在 LuCI 或命令行加的、出口节点）保持不变。
func mergeRoutes(ctx context.Context, lc *local.Client, old, routes []string) error {
	want, err := parsePrefixes(routes)
	if err != nil {
		return err
	}
	drop, _ := parsePrefixes(old)
	ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	prefs, err := lc.GetPrefs(ectx)
	if err != nil {
		return err
	}
	next := mergePrefixes(prefs.AdvertiseRoutes, drop, want)
	if slices.Equal(next, prefs.AdvertiseRoutes) {
		return nil
	}
	_, err = lc.EditPrefs(ectx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{AdvertiseRoutes: next}, AdvertiseRoutesSet: true})
	return err
}

// mergePrefixes 从 cur 中去掉 drop 里且不在 want 里的条目，再补上 want 中缺的，保持原有顺序。
func mergePrefixes(cur, drop, want []netip.Prefix) []netip.Prefix {
	next := make([]netip.Prefix, 0, len(cur)+len(want))
	for _, p := range cur {
		if slices.Contains(drop, p) && !slices.Contains(want, p) {
			continue
		}
		next = append(next, p)
	}
	for _, p := range want {
		if !slices.Contains(next, p) {
			next = append(next, p)
		}
	}
	return next
}

func parsePrefixes(routes []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(routes))
	for _, r := range routes {
		p, err := netip.ParsePrefix(strings.TrimSpace(r))
		if err != nil {
			return nil, i18n.Errorf("非法 CIDR %q: %w", r, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// SetBackend 切换网络后端：先停止当前后端，切换后立即用新后端启动。
// 切到 embedded 时若没有内嵌节点的登录状态，停在“未登录”，等待输入 auth key。
func (n *Node) SetBackend(ctx context.Context, backend string) error {
	if backend != BackendSystem && backend != BackendEmbedded {
		return i18n.Errorf("未知的网络后端 %q", backend)
	}
	if backend == n.Backend() {
		return nil
	}
	if err := n.Stop(); err != nil {
		n.log.Warn(i18n.T("停止节点"), "err", err)
	}
	n.mu.Lock()
	n.cfg.Backend = backend
	n.snap = Snapshot{
		State:         StateNotConfigured,
		Backend:       backend,
		Hostname:      n.cfg.Hostname,
		ControlURL:    n.cfg.ControlURL,
		AdvertiseTags: append([]string(nil), n.cfg.Tags...),
		Since:         time.Now(),
	}
	if backend == BackendSystem {
		n.snap.Hostname = ""
	}
	n.msg = nil
	n.sys = systs.Info{}
	n.mu.Unlock()
	n.log.Info(i18n.T("已切换网络后端"), "backend", backend)
	if n.HasState() {
		return n.Start(ctx, "", nil)
	}
	n.emit()
	return nil
}
