// Package bridge 把串口桥接到 TCP。每个桥接独占一个串口和一个 TCP 端口。
//
// 运行模型：串口在桥接启用期间始终保持打开（设备拔出时等待重插），
// TCP 监听始终存在，同一时刻只允许一个客户端。串口数据同时写入最近输出环
// 和所有订阅者（网页终端），供回显与旁观。
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"

	"rsagent/internal/config"
	"rsagent/internal/rfc2217"
)

// State 是桥接的运行状态。
type State string

const (
	StateDisabled       State = "Disabled"       // 配置为不启用
	StateWaitingNetwork State = "WaitingNetwork" // 节点未运行，无法监听
	StateWaitingDevice  State = "WaitingDevice"  // 串口不存在或被占用，等待重试
	StateListening      State = "Listening"      // 串口已打开，等待客户端
	StateConnected      State = "Connected"      // 有客户端
	StateError          State = "Error"
)

// Status 是桥接状态快照。
type Status struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Port         string    `json:"port"`         // 配置的串口
	ResolvedPort string    `json:"resolvedPort"` // 实际打开的串口
	TCPPort      int       `json:"tcpPort"`
	LocalPort    int       `json:"localPort,omitempty"`
	Mode         string    `json:"mode"`
	Baud         int       `json:"baud"`
	Active       string    `json:"active"`   // 实际生效的参数，如 115200 8N1
	ParamsBy     string    `json:"paramsBy"` // config | client
	DTR          bool      `json:"dtr"`
	RTS          bool      `json:"rts"`
	Enabled      bool      `json:"enabled"`
	State        State     `json:"state"`
	DeviceOpen   bool      `json:"deviceOpen"`
	Client       string    `json:"client,omitempty"`
	ClientSince  time.Time `json:"clientSince,omitzero"`
	FromDevice   int64     `json:"fromDevice"` // 串口 → TCP 字节数
	ToDevice     int64     `json:"toDevice"`   // TCP → 串口 字节数
	Viewers      int       `json:"viewers"`    // 网页终端订阅数
	LastError    string    `json:"lastError,omitempty"`
}

// ListenFunc 创建 TCP 监听，由 tsnet 节点提供。
type ListenFunc func(network, addr string) (net.Listener, error)

// WhoIsFunc 把来源地址解析成可读的身份标签。
type WhoIsFunc func(ctx context.Context, remoteAddr string) string

const (
	deviceRetryInterval = 3 * time.Second
	listenRetryInterval = 5 * time.Second
	keepAlivePeriod     = 10 * time.Second
	recentBufferSize    = 64 << 10
	subscriberBuffer    = 256
)

// Bridge 是一个串口到 TCP 的桥接实例。
type Bridge struct {
	cfg   config.Bridge
	log   *slog.Logger
	whois WhoIsFunc
	onChg func()

	mu           sync.Mutex
	state        State
	resolved     string
	lastErr      string
	port         serial.Port
	active       rfc2217.Mode
	paramsBy     string
	dtr, rts     bool
	listeners    []net.Listener
	client       io.ReadWriteCloser
	clientNet    net.Conn
	clientLabel  string
	clientSince  time.Time
	clientCancel context.CancelFunc
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	subs         map[chan []byte]struct{}

	fromDevice atomic.Int64
	toDevice   atomic.Int64
	recent     *recentRing
}

func newBridge(cfg config.Bridge, log *slog.Logger, whois WhoIsFunc, onChange func()) *Bridge {
	b := &Bridge{
		cfg:      cfg,
		log:      log.With("bridge", cfg.ID),
		whois:    whois,
		onChg:    onChange,
		state:    StateDisabled,
		recent:   newRecentRing(recentBufferSize),
		subs:     map[chan []byte]struct{}{},
		active:   configMode(cfg),
		paramsBy: "config",
		dtr:      true,
		rts:      true,
	}
	if cfg.Enabled {
		b.state = StateWaitingNetwork
	}
	return b
}

func configMode(cfg config.Bridge) rfc2217.Mode {
	p := byte('N')
	if cfg.Parity != "" {
		p = cfg.Parity[0]
	}
	return rfc2217.Mode{Baud: cfg.Baud, DataBits: cfg.DataBits, Parity: p, StopBits: cfg.StopBits}
}

// Status 返回状态快照。
func (b *Bridge) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Status{
		ID:           b.cfg.ID,
		Name:         b.cfg.Name,
		Port:         b.cfg.Port,
		ResolvedPort: b.resolved,
		TCPPort:      b.cfg.TCPPort,
		LocalPort:    b.cfg.LocalPort,
		Mode:         b.cfg.Mode,
		Baud:         b.cfg.Baud,
		Active:       b.active.String(),
		ParamsBy:     b.paramsBy,
		DTR:          b.dtr,
		RTS:          b.rts,
		Enabled:      b.cfg.Enabled,
		State:        b.state,
		DeviceOpen:   b.port != nil,
		Client:       b.clientLabel,
		ClientSince:  b.clientSince,
		FromDevice:   b.fromDevice.Load(),
		ToDevice:     b.toDevice.Load(),
		Viewers:      len(b.subs),
		LastError:    b.lastErr,
	}
}

// Recent 返回最近的串口输出。
func (b *Bridge) Recent() []byte { return b.recent.Bytes() }

// Subscribe 订阅串口输出，用于网页终端。慢消费者会丢数据而不是阻塞串口读取。
func (b *Bridge) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	b.notify()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		b.notify()
	}
}

// WriteDevice 直接向串口写入，供网页终端输入使用。
func (b *Bridge) WriteDevice(p []byte) error {
	b.mu.Lock()
	port := b.port
	b.mu.Unlock()
	if port == nil {
		return errors.New("串口未打开")
	}
	n, err := port.Write(p)
	b.toDevice.Add(int64(n))
	return err
}

// start 启动桥接的串口循环与 TCP 监听循环。
func (b *Bridge) start(listen ListenFunc) {
	b.mu.Lock()
	if b.cancel != nil {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.setStateLocked(StateWaitingDevice, "")
	b.mu.Unlock()
	b.notify()

	b.wg.Add(2)
	go b.deviceLoop(ctx)
	go b.listenLoop(ctx, listen, fmt.Sprintf(":%d", b.cfg.TCPPort), "tailnet")
	if b.cfg.LocalPort > 0 {
		b.wg.Add(1)
		go b.listenLoop(ctx, net.Listen, fmt.Sprintf("127.0.0.1:%d", b.cfg.LocalPort), "local")
	}
}

// stop 停止桥接并释放串口、监听和客户端。
func (b *Bridge) stop(next State) {
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	lns := b.listeners
	b.listeners = nil
	b.mu.Unlock()
	if cancel == nil {
		b.mu.Lock()
		b.setStateLocked(next, "")
		b.mu.Unlock()
		b.notify()
		return
	}
	cancel()
	for _, ln := range lns {
		_ = ln.Close()
	}
	b.dropClient("桥接停止")
	b.closePort()
	b.wg.Wait()
	b.mu.Lock()
	b.setStateLocked(next, "")
	b.mu.Unlock()
	b.notify()
}

// kick 断开当前客户端。
func (b *Bridge) kick() bool {
	return b.dropClient("被管理员断开")
}

func (b *Bridge) setStateLocked(s State, errMsg string) {
	b.state = s
	if errMsg != "" {
		b.lastErr = errMsg
	}
}

func (b *Bridge) notify() {
	if b.onChg != nil {
		b.onChg()
	}
}

// ---- 串口侧 ----

func (b *Bridge) deviceLoop(ctx context.Context) {
	defer b.wg.Done()
	for ctx.Err() == nil {
		if err := b.openPort(); err != nil {
			b.mu.Lock()
			msg := err.Error()
			if isNotFound(err) {
				msg = "串口不存在，等待设备接入"
			} else if isBusy(err) {
				msg = "串口被其他程序占用"
			}
			changed := b.state != StateWaitingDevice || b.lastErr != msg
			b.setStateLocked(StateWaitingDevice, msg)
			b.mu.Unlock()
			if changed {
				b.log.Warn("打开串口失败", "err", err)
				b.notify()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(deviceRetryInterval):
			}
			continue
		}

		b.mu.Lock()
		b.lastErr = ""
		if b.client != nil {
			b.setStateLocked(StateConnected, "")
		} else {
			b.setStateLocked(StateListening, "")
		}
		b.mu.Unlock()
		b.log.Info("串口已打开", "port", b.resolvedName(), "params", b.activeMode().String())
		b.notify()

		b.readPort(ctx)
		b.closePort()
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		b.setStateLocked(StateWaitingDevice, "串口断开，等待重连")
		b.mu.Unlock()
		b.log.Warn("串口断开，等待重连")
		b.notify()
		select {
		case <-ctx.Done():
			return
		case <-time.After(deviceRetryInterval):
		}
	}
}

func (b *Bridge) openPort() error {
	name, err := resolvePort(b.cfg)
	if err != nil {
		return err
	}
	b.mu.Lock()
	mode := b.active
	dtr, rts := b.dtr, b.rts
	b.mu.Unlock()
	sm, err := serialMode(mode)
	if err != nil {
		return err
	}
	sm.InitialStatusBits = &serial.ModemOutputBits{DTR: dtr, RTS: rts}
	p, err := serial.Open(name, sm)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.port = p
	b.resolved = name
	b.mu.Unlock()
	return nil
}

func (b *Bridge) closePort() {
	b.mu.Lock()
	p := b.port
	b.port = nil
	b.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
}

func (b *Bridge) resolvedName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resolved
}

func (b *Bridge) activeMode() rfc2217.Mode {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// readPort 把串口数据转发给客户端、订阅者和最近输出环，直到出错或被关闭。
func (b *Bridge) readPort(ctx context.Context) {
	b.mu.Lock()
	p := b.port
	b.mu.Unlock()
	if p == nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			data := buf[:n]
			b.fromDevice.Add(int64(n))
			b.recent.Write(data)
			b.mu.Lock()
			c := b.client
			var subs []chan []byte
			for ch := range b.subs {
				subs = append(subs, ch)
			}
			b.mu.Unlock()
			if len(subs) > 0 {
				cp := append([]byte(nil), data...)
				for _, ch := range subs {
					select {
					case ch <- cp:
					default:
					}
				}
			}
			if c != nil {
				if _, werr := c.Write(data); werr != nil {
					b.dropClient("写入客户端失败: " + werr.Error())
				}
			}
		}
		if err != nil {
			// Windows 上 SetCommState（客户端改波特率）会中止挂起的 ReadFile，库把它报成 PortClosed。
			// 串口句柄仍由我们持有且控制线还能读到，就当作瞬时错误继续读，不要把设备判成断开。
			var pe *serial.PortError
			if errors.As(err, &pe) && pe.Code() == serial.PortClosed && b.holdsPort(p) {
				if _, serr := p.GetModemStatusBits(); serr == nil {
					b.log.Debug("串口读取被中止后恢复", "err", err)
					continue
				}
			}
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				b.log.Debug("串口读取结束", "err", err)
			}
			return
		}
	}
}

func (b *Bridge) holdsPort(p serial.Port) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.port == p
}

// ---- rfc2217.Port 实现 ----

// GetMode 返回当前生效的参数。
func (b *Bridge) GetMode() rfc2217.Mode { return b.activeMode() }

// SetMode 由 RFC2217 客户端调用，修改串口参数。
func (b *Bridge) SetMode(m rfc2217.Mode) error {
	sm, err := serialMode(m)
	if err != nil {
		return err
	}
	b.mu.Lock()
	p := b.port
	b.mu.Unlock()
	if p != nil {
		if err := p.SetMode(sm); err != nil {
			return err
		}
	}
	b.mu.Lock()
	changed := b.active != m
	b.active = m
	b.paramsBy = "client"
	b.mu.Unlock()
	if changed {
		b.log.Info("客户端修改串口参数", "params", m.String())
	}
	return nil
}

// SetDTR 设置 DTR。
func (b *Bridge) SetDTR(on bool) error {
	b.mu.Lock()
	p := b.port
	b.dtr = on
	b.mu.Unlock()
	if p == nil {
		return errors.New("串口未打开")
	}
	return p.SetDTR(on)
}

// SetRTS 设置 RTS。
func (b *Bridge) SetRTS(on bool) error {
	b.mu.Lock()
	p := b.port
	b.rts = on
	b.mu.Unlock()
	if p == nil {
		return errors.New("串口未打开")
	}
	return p.SetRTS(on)
}

// DTR 返回最近设置的 DTR。
func (b *Bridge) DTR() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dtr
}

// RTS 返回最近设置的 RTS。
func (b *Bridge) RTS() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rts
}

// Break 发送 break 信号。
func (b *Bridge) Break(d time.Duration) error {
	b.mu.Lock()
	p := b.port
	b.mu.Unlock()
	if p == nil {
		return errors.New("串口未打开")
	}
	return p.Break(d)
}

// Purge 清空缓冲区。
func (b *Bridge) Purge(rx, tx bool) error {
	b.mu.Lock()
	p := b.port
	b.mu.Unlock()
	if p == nil {
		return errors.New("串口未打开")
	}
	var err error
	if rx {
		err = p.ResetInputBuffer()
	}
	if tx {
		if e := p.ResetOutputBuffer(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

// ModemStatus 读取输入控制线。
func (b *Bridge) ModemStatus() (rfc2217.ModemStatus, error) {
	b.mu.Lock()
	p := b.port
	b.mu.Unlock()
	if p == nil {
		return rfc2217.ModemStatus{}, errors.New("串口未打开")
	}
	bits, err := p.GetModemStatusBits()
	if err != nil {
		return rfc2217.ModemStatus{}, err
	}
	return rfc2217.ModemStatus{CTS: bits.CTS, DSR: bits.DSR, RI: bits.RI, DCD: bits.DCD}, nil
}

// restoreConfigMode 客户端断开后恢复配置的参数，并把 DTR/RTS 拉回默认的高电平。
func (b *Bridge) restoreConfigMode() {
	b.mu.Lock()
	changedMode := b.paramsBy == "client"
	changedLines := !b.dtr || !b.rts
	if !changedMode && !changedLines {
		b.mu.Unlock()
		return
	}
	b.active = configMode(b.cfg)
	b.paramsBy = "config"
	b.dtr, b.rts = true, true
	p := b.port
	m := b.active
	b.mu.Unlock()
	if p != nil {
		if changedMode {
			if sm, err := serialMode(m); err == nil {
				if err := p.SetMode(sm); err != nil {
					b.log.Debug("恢复配置参数失败", "err", err)
				}
			}
		}
		if changedLines {
			_ = p.SetDTR(true)
			_ = p.SetRTS(true)
		}
	}
	b.log.Info("已恢复配置的串口参数", "params", m.String())
	b.notify()
}

// ---- TCP 侧 ----

func (b *Bridge) listenLoop(ctx context.Context, listen ListenFunc, addr, kind string) {
	defer b.wg.Done()
	for ctx.Err() == nil {
		ln, err := listen("tcp", addr)
		if err != nil {
			b.mu.Lock()
			b.setStateLocked(StateError, kind+" 监听失败: "+err.Error())
			b.mu.Unlock()
			b.log.Error("TCP 监听失败", "kind", kind, "addr", addr, "err", err)
			b.notify()
			select {
			case <-ctx.Done():
				return
			case <-time.After(listenRetryInterval):
			}
			continue
		}
		b.mu.Lock()
		b.listeners = append(b.listeners, ln)
		b.mu.Unlock()
		b.log.Info("TCP 已监听", "kind", kind, "addr", addr)

		for {
			c, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				b.log.Warn("接受连接失败", "kind", kind, "err", err)
				break
			}
			b.acceptClient(ctx, c, kind)
		}
		_ = ln.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetryInterval):
		}
	}
}

func (b *Bridge) acceptClient(ctx context.Context, c net.Conn, kind string) {
	remote := c.RemoteAddr().String()
	label := remote
	if kind == "local" {
		label = "本机 " + remote
	} else if b.whois != nil {
		wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if who := b.whois(wctx, remote); who != "" {
			label = who + " (" + remote + ")"
		}
		cancel()
	}

	b.mu.Lock()
	if b.client != nil {
		if b.cfg.ClientPolicy == "reject" {
			b.mu.Unlock()
			b.log.Info("已有客户端，拒绝新连接", "new", label)
			_, _ = c.Write([]byte("rsagent: port busy\r\n"))
			_ = c.Close()
			return
		}
		b.mu.Unlock()
		b.dropClient("被新连接替换")
		b.mu.Lock()
	}
	tuneConn(c)

	var client io.ReadWriteCloser = c
	if b.cfg.Mode == "rfc2217" {
		srv := rfc2217.NewServer(c, b, b.log)
		srv.OnChange = b.notify
		b.mu.Unlock()
		if err := srv.Start(); err != nil {
			b.log.Warn("RFC2217 协商发送失败", "err", err)
			_ = c.Close()
			return
		}
		b.mu.Lock()
		client = srv
	}

	cctx, ccancel := context.WithCancel(ctx)
	b.client = client
	b.clientNet = c
	b.clientLabel = label
	b.clientSince = time.Now()
	b.clientCancel = ccancel
	if b.port != nil {
		b.setStateLocked(StateConnected, "")
	}
	b.mu.Unlock()
	b.log.Info("客户端已连接", "client", label, "mode", b.cfg.Mode)
	b.notify()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.serveClient(cctx, client, c)
	}()
}

// serveClient 把客户端数据写入串口，直到连接断开或空闲超时。
func (b *Bridge) serveClient(ctx context.Context, client io.ReadWriteCloser, nc net.Conn) {
	idle := time.Duration(b.cfg.IdleTimeoutSec) * time.Second
	buf := make([]byte, 4096)
	for {
		if idle > 0 {
			_ = nc.SetReadDeadline(time.Now().Add(idle))
		}
		n, err := client.Read(buf)
		if n > 0 {
			b.toDevice.Add(int64(n))
			b.mu.Lock()
			p := b.port
			b.mu.Unlock()
			if p != nil {
				if _, werr := p.Write(buf[:n]); werr != nil {
					b.log.Debug("写入串口失败", "err", werr)
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reason := "客户端断开"
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				reason = "空闲超时"
			} else if !errors.Is(err, io.EOF) {
				reason = "读取客户端失败: " + err.Error()
			}
			b.dropClientConn(client, reason)
			return
		}
	}
}

// dropClient 断开当前客户端。
func (b *Bridge) dropClient(reason string) bool {
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return false
	}
	b.dropClientConn(c, reason)
	return true
}

// dropClientConn 只在 c 仍是当前客户端时断开，避免误杀替换后的新连接。
func (b *Bridge) dropClientConn(c io.ReadWriteCloser, reason string) {
	b.mu.Lock()
	if b.client != c {
		b.mu.Unlock()
		return
	}
	label := b.clientLabel
	cancel := b.clientCancel
	b.client = nil
	b.clientNet = nil
	b.clientLabel = ""
	b.clientSince = time.Time{}
	b.clientCancel = nil
	if b.state == StateConnected {
		b.setStateLocked(StateListening, "")
	}
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = c.Close()
	b.restoreConfigMode()
	b.log.Info("客户端已断开", "client", label, "reason", reason)
	b.notify()
}

// tuneConn 关闭 Nagle 并开启 keepalive。tsnet 的连接不是 *net.TCPConn，用接口断言。
func tuneConn(c net.Conn) {
	if nd, ok := c.(interface{ SetNoDelay(bool) error }); ok {
		_ = nd.SetNoDelay(true)
	}
	if ka, ok := c.(interface{ SetKeepAlive(bool) error }); ok {
		_ = ka.SetKeepAlive(true)
	}
	if kp, ok := c.(interface{ SetKeepAlivePeriod(time.Duration) error }); ok {
		_ = kp.SetKeepAlivePeriod(keepAlivePeriod)
	}
}
