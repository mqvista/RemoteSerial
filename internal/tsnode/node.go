// Package tsnode 封装内嵌的 tsnet 节点：登录、状态机、持久化、监听与身份查询。
//
// 状态持久化由 tsnet 自己完成，存放在 StateDir/tailscaled.state。再次启动时
// tsnet 发现已有状态就忽略 AuthKey，所以"记住登录"不需要额外代码。
// auth key 只在首次入网时经过内存，不写入任何文件。
package tsnode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	_ "tailscale.com/feature/oauthkey" // 让 tskey-client-… 形式的 OAuth client secret 可当 auth key 用
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// State 是对界面暴露的节点状态。
type State string

const (
	StateNotConfigured    State = "NotConfigured"    // 没有持久化状态，也没在运行
	StateStarting         State = "Starting"         // 正在连接控制服务器
	StateNeedsLogin       State = "NeedsLogin"       // 控制服务器要求登录，auth key 无效或缺失
	StateNeedsMachineAuth State = "NeedsMachineAuth" // 等待管理员在后台批准
	StateRunning          State = "Running"
	StateStopped          State = "Stopped" // 已有状态但节点未运行
	StateError            State = "Error"
)

// Config 是节点参数，来自配置文件。
type Config struct {
	StateDir        string
	Hostname        string
	ControlURL      string
	UDPPort         uint16
	AdvertiseRoutes []string
}

// Snapshot 是某一时刻的节点状态，可直接序列化给界面。
type Snapshot struct {
	State      State      `json:"state"`
	Hostname   string     `json:"hostname"`
	ControlURL string     `json:"controlURL"`
	HasState   bool       `json:"hasState"`
	IP         string     `json:"ip,omitempty"`
	IPv6       string     `json:"ipv6,omitempty"`
	DNSName    string     `json:"dnsName,omitempty"`
	Tailnet    string     `json:"tailnet,omitempty"`
	LoginName  string     `json:"loginName,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	KeyExpiry  *time.Time `json:"keyExpiry,omitempty"`
	HomeRelay  string     `json:"homeRelay,omitempty"` // 当前首选 DERP 区域代码
	Message    string     `json:"message,omitempty"`   // 最近一次错误或提示
	Since      time.Time  `json:"since"`               // 进入当前状态的时间
}

// Peer 是 tailnet 中一个对端的摘要，用于诊断直连还是中转。
type Peer struct {
	Name          string    `json:"name"`
	HostName      string    `json:"hostName"`
	IP            string    `json:"ip"`
	OS            string    `json:"os"`
	Online        bool      `json:"online"`
	Direct        bool      `json:"direct"`
	CurAddr       string    `json:"curAddr,omitempty"`
	Relay         string    `json:"relay,omitempty"`
	LastHandshake time.Time `json:"lastHandshake"`
	RxBytes       int64     `json:"rxBytes"`
	TxBytes       int64     `json:"txBytes"`
}

// Identity 是通过 WhoIs 查到的请求方身份。
type Identity struct {
	LoginName   string   `json:"loginName"`
	DisplayName string   `json:"displayName"`
	Node        string   `json:"node"`
	Tags        []string `json:"tags,omitempty"`
}

// Hooks 是节点生命周期回调，app 在这里创建和回收 tailnet 侧的监听。
type Hooks struct {
	OnStarted func(n *Node)  // tsnet.Server 已 Start，可以 Listen
	OnStopped func()         // tsnet.Server 已 Close，监听已失效
	OnChange  func(Snapshot) // 任何状态变化
}

// Node 是一个内嵌 tsnet 节点。零值不可用，用 New 创建。
type Node struct {
	cfg   Config
	log   *slog.Logger
	hooks Hooks

	mu     sync.Mutex
	srv    *tsnet.Server
	lc     *local.Client
	snap   Snapshot
	cancel context.CancelFunc
}

// New 创建节点对象，不启动。
func New(cfg Config, log *slog.Logger, hooks Hooks) *Node {
	n := &Node{cfg: cfg, log: log, hooks: hooks}
	n.snap = Snapshot{
		State:      StateNotConfigured,
		Hostname:   cfg.Hostname,
		ControlURL: cfg.ControlURL,
		HasState:   n.HasState(),
		Since:      time.Now(),
	}
	if n.snap.HasState {
		n.snap.State = StateStopped
	}
	return n
}

// Config 返回当前配置副本。
func (n *Node) Config() Config {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cfg
}

// UpdateConfig 更新主机名和控制服务器地址，仅在节点未运行时允许。
func (n *Node) UpdateConfig(hostname, controlURL string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.srv != nil {
		return errors.New("节点运行中，先停止再修改")
	}
	if hostname != "" {
		n.cfg.Hostname = hostname
		n.snap.Hostname = hostname
	}
	n.cfg.ControlURL = controlURL
	n.snap.ControlURL = controlURL
	return nil
}

// SetAdvertiseRoutesConfig 更新配置中的子网路由，运行中则立即生效。
func (n *Node) SetAdvertiseRoutesConfig(ctx context.Context, routes []string) error {
	n.mu.Lock()
	n.cfg.AdvertiseRoutes = append([]string(nil), routes...)
	lc := n.lc
	n.mu.Unlock()
	if lc == nil {
		return nil
	}
	return applyRoutes(ctx, lc, routes)
}

// HasState 判断是否有持久化的节点状态。
func (n *Node) HasState() bool {
	_, err := os.Stat(filepath.Join(n.cfg.StateDir, "tailscaled.state"))
	return err == nil
}

// Snapshot 返回当前状态副本。
func (n *Node) Snapshot() Snapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.snap
	s.HasState = n.HasState()
	return s
}

// Running 判断节点是否处于 Running。
func (n *Node) Running() bool {
	return n.Snapshot().State == StateRunning
}

// Start 启动节点。首次入网需要 authKey；已有状态时传空即可。
//
// tags 是入网时申请的 tag（如 tag:serial-agent）。authKey 是 OAuth client secret
// （tskey-client-…）时必须给 tags，tsnet 会用它向 API 换取一次性 auth key；普通
// auth key 可不给，由 key 自带的 tag 决定。
func (n *Node) Start(ctx context.Context, authKey string, tags []string) error {
	n.mu.Lock()
	if n.srv != nil {
		n.mu.Unlock()
		return errors.New("节点已在运行")
	}
	if authKey == "" && !n.HasState() {
		n.mu.Unlock()
		return errors.New("尚未登录，需要 auth key")
	}
	if strings.HasPrefix(authKey, "tskey-client-") && len(tags) == 0 {
		n.mu.Unlock()
		return errors.New("使用 OAuth client secret 登录必须指定 tag，例如 tag:serial-agent")
	}
	if strings.HasPrefix(authKey, "tskey-client-") && !strings.Contains(authKey, "?") {
		// 不加 ephemeral=false 时 OAuth 换出的 key 默认建临时节点，断线即被删除。
		authKey += "?ephemeral=false&preauthorized=true"
	}
	if err := os.MkdirAll(n.cfg.StateDir, 0o700); err != nil {
		n.mu.Unlock()
		return fmt.Errorf("创建状态目录: %w", err)
	}

	srv := &tsnet.Server{
		Dir:        n.cfg.StateDir,
		Hostname:   n.cfg.Hostname,
		AuthKey:    authKey,
		ControlURL: n.cfg.ControlURL,
		Port:       n.cfg.UDPPort,
		Ephemeral:  false,
		Logf:       n.tsLogf(slog.LevelDebug),
		UserLogf:   n.tsLogf(slog.LevelInfo),
	}
	if authKey != "" && len(tags) > 0 {
		srv.AdvertiseTags = append([]string(nil), tags...)
	}
	n.srv = srv
	n.setStateLocked(StateStarting, "")
	n.mu.Unlock()
	n.emit()

	if err := srv.Start(); err != nil {
		n.failStart(srv, fmt.Errorf("tsnet 启动失败: %w", err))
		return err
	}
	lc, err := srv.LocalClient()
	if err != nil {
		n.failStart(srv, fmt.Errorf("获取本地客户端: %w", err))
		return err
	}

	wctx, cancel := context.WithCancel(context.Background())
	n.mu.Lock()
	n.lc = lc
	n.cancel = cancel
	n.mu.Unlock()

	if n.hooks.OnStarted != nil {
		n.hooks.OnStarted(n)
	}
	go n.watch(wctx, lc)
	go n.up(wctx, srv, lc)
	return nil
}

func (n *Node) failStart(srv *tsnet.Server, err error) {
	_ = srv.Close()
	n.mu.Lock()
	n.srv = nil
	n.lc = nil
	n.setStateLocked(StateError, err.Error())
	n.mu.Unlock()
	n.emit()
	n.log.Error("节点启动失败", "err", err)
}

// Stop 停止节点但保留持久化状态，下次 Start 不需要 auth key。
func (n *Node) Stop() error {
	n.mu.Lock()
	srv := n.srv
	cancel := n.cancel
	n.srv = nil
	n.lc = nil
	n.cancel = nil
	n.mu.Unlock()
	if srv == nil {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	err := srv.Close()
	if n.hooks.OnStopped != nil {
		n.hooks.OnStopped()
	}
	n.mu.Lock()
	if n.HasState() {
		n.setStateLocked(StateStopped, "")
	} else {
		n.setStateLocked(StateNotConfigured, "")
	}
	n.mu.Unlock()
	n.emit()
	n.log.Info("节点已停止")
	return err
}

// Logout 退出登录并删除持久化状态，回到 NotConfigured。
func (n *Node) Logout(ctx context.Context) error {
	n.mu.Lock()
	lc := n.lc
	n.mu.Unlock()
	if lc != nil {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := lc.Logout(lctx); err != nil {
			n.log.Warn("向控制服务器注销失败，继续本地重置", "err", err)
		}
		cancel()
	}
	_ = n.Stop()
	if err := os.RemoveAll(n.cfg.StateDir); err != nil {
		return fmt.Errorf("删除状态目录: %w", err)
	}
	n.mu.Lock()
	n.setStateLocked(StateNotConfigured, "")
	n.snap.IP, n.snap.IPv6, n.snap.DNSName, n.snap.Tailnet, n.snap.LoginName = "", "", "", "", ""
	n.snap.Tags, n.snap.KeyExpiry = nil, nil
	n.mu.Unlock()
	n.emit()
	n.log.Info("已退出登录并清除节点状态")
	return nil
}

// Listen 在 tailnet 地址上监听。节点未启动时返回错误。
func (n *Node) Listen(network, addr string) (net.Listener, error) {
	n.mu.Lock()
	srv := n.srv
	n.mu.Unlock()
	if srv == nil {
		return nil, errors.New("节点未启动")
	}
	return srv.Listen(network, addr)
}

// WhoIs 查询某个 tailnet 来源地址对应的用户和设备。
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (*Identity, error) {
	n.mu.Lock()
	lc := n.lc
	n.mu.Unlock()
	if lc == nil {
		return nil, errors.New("节点未启动")
	}
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	who, err := lc.WhoIs(wctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	id := &Identity{}
	if who.UserProfile != nil {
		id.LoginName = who.UserProfile.LoginName
		id.DisplayName = who.UserProfile.DisplayName
	}
	if who.Node != nil {
		id.Node = strings.TrimSuffix(who.Node.Name, ".")
		if who.Node.Tags != nil {
			id.Tags = append([]string(nil), who.Node.Tags...)
		}
	}
	return id, nil
}

// Peers 返回对端列表，用于诊断直连与中转。
func (n *Node) Peers(ctx context.Context) ([]Peer, error) {
	n.mu.Lock()
	lc := n.lc
	n.mu.Unlock()
	if lc == nil {
		return nil, errors.New("节点未启动")
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := lc.Status(sctx)
	if err != nil {
		return nil, err
	}
	out := make([]Peer, 0, len(st.Peer))
	for _, p := range st.Peer {
		peer := Peer{
			Name:          strings.TrimSuffix(p.DNSName, "."),
			HostName:      p.HostName,
			OS:            p.OS,
			Online:        p.Online,
			Direct:        p.CurAddr != "",
			CurAddr:       p.CurAddr,
			Relay:         p.Relay,
			LastHandshake: p.LastHandshake,
			RxBytes:       p.RxBytes,
			TxBytes:       p.TxBytes,
		}
		for _, ip := range p.TailscaleIPs {
			if ip.Is4() {
				peer.IP = ip.String()
				break
			}
		}
		out = append(out, peer)
	}
	return out, nil
}

// ---- 内部 ----

func (n *Node) tsLogf(level slog.Level) func(string, ...any) {
	return func(format string, args ...any) {
		if !n.log.Enabled(context.Background(), level) {
			return
		}
		n.log.Log(context.Background(), level, strings.TrimSpace(fmt.Sprintf(format, args...)), "src", "tsnet")
	}
}

func (n *Node) setStateLocked(s State, msg string) {
	if n.snap.State != s {
		n.snap.Since = time.Now()
	}
	n.snap.State = s
	n.snap.Message = msg
}

func (n *Node) emit() {
	if n.hooks.OnChange != nil {
		n.hooks.OnChange(n.Snapshot())
	}
}

// watch 订阅 IPN 总线，把后端状态映射到 Snapshot。
func (n *Node) watch(ctx context.Context, lc *local.Client) {
	for ctx.Err() == nil {
		w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			n.log.Debug("订阅 IPN 总线失败，重试", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for {
			nt, err := w.Next()
			if err != nil {
				break
			}
			if nt.ErrMessage != nil && *nt.ErrMessage != "" {
				n.mu.Lock()
				n.snap.Message = *nt.ErrMessage
				n.mu.Unlock()
				n.log.Warn("tailscale 后端报告错误", "msg", *nt.ErrMessage)
				n.emit()
			}
			if nt.State != nil {
				n.applyState(ctx, lc, *nt.State)
			}
		}
		_ = w.Close()
	}
}

func (n *Node) applyState(ctx context.Context, lc *local.Client, st ipn.State) {
	var s State
	msg := ""
	switch st {
	case ipn.Running:
		s = StateRunning
	case ipn.NeedsLogin:
		s = StateNeedsLogin
		msg = "控制服务器要求登录：auth key 无效、已过期或已被撤销"
	case ipn.NeedsMachineAuth:
		s = StateNeedsMachineAuth
		msg = "等待管理员在 Tailscale 后台批准此设备"
	case ipn.Stopped:
		s = StateStopped
	case ipn.InUseOtherUser:
		s = StateError
		msg = "节点被其他用户占用"
	default:
		s = StateStarting
	}

	n.mu.Lock()
	prev := n.snap.State
	// Starting 期间后端会短暂经过 NeedsLogin，auth key 正常时很快离开。
	// 只在真正停留时才切换状态，避免界面闪烁：这里延迟判断交给 up() 的超时。
	if s == StateNeedsLogin && prev == StateStarting && n.snap.Message == "" {
		n.mu.Unlock()
		return
	}
	n.setStateLocked(s, msg)
	n.mu.Unlock()

	if s == StateRunning {
		n.refreshSelf(ctx, lc)
		if routes := n.Config().AdvertiseRoutes; len(routes) > 0 {
			if err := applyRoutes(ctx, lc, routes); err != nil {
				n.log.Warn("宣告子网路由失败", "err", err)
			}
		}
	}
	if prev != s {
		n.log.Info("节点状态变化", "from", prev, "to", s)
	}
	n.emit()
}

// up 调用 tsnet.Up 等待上线，用于把后端错误和超时翻译成界面提示。
func (n *Node) up(ctx context.Context, srv *tsnet.Server, lc *local.Client) {
	uctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	_, err := srv.Up(uctx)
	if err == nil || ctx.Err() != nil {
		return
	}
	n.mu.Lock()
	if n.snap.State == StateRunning {
		n.mu.Unlock()
		return
	}
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		switch n.snap.State {
		case StateNeedsMachineAuth:
			msg = "等待管理员在 Tailscale 后台批准此设备"
		default:
			msg = "上线超时：auth key 可能无效或已过期，或无法连接控制服务器"
			n.snap.State = StateNeedsLogin
		}
	}
	n.snap.Message = msg
	n.mu.Unlock()
	n.log.Warn("节点上线未完成", "err", err)
	n.emit()

	// 再查一次后端真实状态，避免错过通知。
	if st, err := lc.StatusWithoutPeers(context.Background()); err == nil && st.BackendState == ipn.Running.String() {
		n.applyState(ctx, lc, ipn.Running)
	}
}

func (n *Node) refreshSelf(ctx context.Context, lc *local.Client) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := lc.StatusWithoutPeers(sctx)
	if err != nil {
		n.log.Warn("读取节点状态失败", "err", err)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.snap.IP, n.snap.IPv6 = "", ""
	for _, ip := range st.TailscaleIPs {
		if ip.Is4() && n.snap.IP == "" {
			n.snap.IP = ip.String()
		} else if ip.Is6() && n.snap.IPv6 == "" {
			n.snap.IPv6 = ip.String()
		}
	}
	if st.Self != nil {
		n.snap.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		n.snap.KeyExpiry = st.Self.KeyExpiry
		n.snap.HomeRelay = st.Self.Relay
		n.snap.Tags = nil
		if st.Self.Tags != nil {
			n.snap.Tags = st.Self.Tags.AsSlice()
		}
		// 带 tag 的节点归属于"tagged-devices"伪用户，其 LoginName 就是节点名，没有意义，留空。
		n.snap.LoginName = ""
		if up, ok := st.User[st.Self.UserID]; ok && len(n.snap.Tags) == 0 {
			n.snap.LoginName = up.LoginName
		}
	}
	if st.CurrentTailnet != nil {
		n.snap.Tailnet = st.CurrentTailnet.Name
	}
	n.snap.Message = ""
}

func applyRoutes(ctx context.Context, lc *local.Client, routes []string) error {
	prefixes := make([]netip.Prefix, 0, len(routes))
	for _, r := range routes {
		p, err := netip.ParsePrefix(strings.TrimSpace(r))
		if err != nil {
			return fmt.Errorf("非法 CIDR %q: %w", r, err)
		}
		prefixes = append(prefixes, p.Masked())
	}
	ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := lc.EditPrefs(ectx, &ipn.MaskedPrefs{
		Prefs:              ipn.Prefs{AdvertiseRoutes: prefixes},
		AdvertiseRoutesSet: true,
	})
	return err
}

// RouteStatus 返回已宣告与已批准的子网路由。
type RouteStatus struct {
	Advertised []string `json:"advertised"`
	Approved   []string `json:"approved"`
}

// Routes 查询子网路由的宣告与批准情况。
func (n *Node) Routes(ctx context.Context) (RouteStatus, error) {
	n.mu.Lock()
	lc := n.lc
	cfgRoutes := append([]string{}, n.cfg.AdvertiseRoutes...)
	n.mu.Unlock()
	rs := RouteStatus{Advertised: cfgRoutes, Approved: []string{}}
	if lc == nil {
		return rs, nil
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := lc.StatusWithoutPeers(sctx)
	if err != nil {
		return rs, err
	}
	rs.Approved = approvedRoutes(st)
	return rs, nil
}

func approvedRoutes(st *ipnstate.Status) []string {
	out := []string{}
	if st == nil || st.Self == nil || st.Self.PrimaryRoutes == nil {
		return out
	}
	for _, p := range st.Self.PrimaryRoutes.AsSlice() {
		out = append(out, p.String())
	}
	return out
}
