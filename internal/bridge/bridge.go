// Package bridge 把串口桥接到 TCP。每个桥接独占一个串口和一个 TCP 端口。
//
// 运行模型：串口在桥接启用期间始终保持打开（设备拔出时等待重插），
// TCP 监听始终存在，同一时刻只允许一个客户端。双向流量带时间戳记入最近历史
// 并分发给订阅者（网页终端），供回显与监看。
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"rsagent/internal/i18n"
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
	// 最近一秒的速率（字节/秒），由 Manager 每秒取样
	RateFromDevice int64         `json:"rateFromDevice"`
	RateToDevice   int64         `json:"rateToDevice"`
	Viewers        int           `json:"viewers"` // 网页终端订阅数
	Capture        CaptureStatus `json:"capture"`
	LastError      string        `json:"lastError,omitempty"`
}

// ListenFunc 创建 TCP 监听，由 tsnet 节点提供。
type ListenFunc func(network, addr string) (net.Listener, error)

// WhoIsFunc 把来源地址解析成可读的身份标签。
type WhoIsFunc func(ctx context.Context, remoteAddr string) string

const (
	deviceRetryInterval = 3 * time.Second
	listenRetryInterval = 5 * time.Second
	keepAlivePeriod     = 10 * time.Second
	subscriberBuffer    = 256
)

// openSerial 打开串口；测试时替换。
var openSerial = serial.Open

// Bridge 是一个串口到 TCP 的桥接实例。
type Bridge struct {
	cfg   config.Bridge
	log   *slog.Logger
	whois WhoIsFunc
	onChg func()

	mu           sync.Mutex
	state        State
	resolved     string
	lastErr      i18n.Text // 渲染状态时按当前语言翻译
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
	cap          *capture
	holder       func(port, self string) string // 返回已打开该串口的其他桥接名称，由 Manager 设置
	wake         chan struct{}                  // 有客户端连上时通知 deviceLoop（OpenOnConnect）
	idleClosed   bool                           // 串口是因为客户端断开而关闭的，不是被拔出（OpenOnConnect）

	fromDevice atomic.Int64
	toDevice   atomic.Int64
	tap        *tap
	rate       rateMeter
}

// rateMeter 记录上次取样的计数，算出最近一个取样周期的速率。
type rateMeter struct {
	mu               sync.Mutex
	at               time.Time
	lastFrom, lastTo int64
	fromRate, toRate int64
}

// sampleRate 取样一次，返回速率是否有变化。
func (b *Bridge) sampleRate(now time.Time) bool {
	from, to := b.fromDevice.Load(), b.toDevice.Load()
	r := &b.rate
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at.IsZero() {
		r.at, r.lastFrom, r.lastTo = now, from, to
		return false
	}
	secs := now.Sub(r.at).Seconds()
	if secs <= 0 {
		return false
	}
	// 计数器在重新打开时可能归零，这时不算出负速率
	fr, tr := int64(float64(max(from-r.lastFrom, 0))/secs+0.5), int64(float64(max(to-r.lastTo, 0))/secs+0.5)
	changed := fr != r.fromRate || tr != r.toRate
	r.fromRate, r.toRate = fr, tr
	r.at, r.lastFrom, r.lastTo = now, from, to
	return changed
}

func (b *Bridge) rates() (from, to int64) {
	b.rate.mu.Lock()
	defer b.rate.mu.Unlock()
	return b.rate.fromRate, b.rate.toRate
}

func newBridge(cfg config.Bridge, log *slog.Logger, whois WhoIsFunc, onChange func()) *Bridge {
	b := &Bridge{
		cfg:      cfg,
		log:      log.With("bridge", cfg.ID),
		whois:    whois,
		onChg:    onChange,
		state:    StateDisabled,
		tap:      newTap(),
		wake:     make(chan struct{}, 1),
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
	capSt := b.Capture()
	viewers := b.tap.count()
	rateFrom, rateTo := b.rates()
	b.mu.Lock()
	defer b.mu.Unlock()
	return Status{
		Capture:        capSt,
		ID:             b.cfg.ID,
		Name:           b.cfg.Name,
		Port:           b.cfg.Port,
		ResolvedPort:   b.resolved,
		TCPPort:        b.cfg.TCPPort,
		LocalPort:      b.cfg.LocalPort,
		Mode:           b.cfg.Mode,
		Baud:           b.cfg.Baud,
		Active:         b.active.String(),
		ParamsBy:       b.paramsBy,
		DTR:            b.dtr,
		RTS:            b.rts,
		Enabled:        b.cfg.Enabled,
		State:          b.state,
		DeviceOpen:     b.port != nil,
		Client:         b.clientLabel,
		ClientSince:    b.clientSince,
		FromDevice:     b.fromDevice.Load(),
		ToDevice:       b.toDevice.Load(),
		RateFromDevice: rateFrom,
		RateToDevice:   rateTo,
		Viewers:        viewers,
		LastError:      i18n.String(b.lastErr),
	}
}

// Subscribe 订阅双向流量，用于网页终端：返回最近的历史与之后的实时数据。
// 慢消费者会丢数据而不是阻塞串口读写。
func (b *Bridge) Subscribe() ([]Chunk, <-chan Chunk, func()) {
	hist, ch := b.tap.subscribe()
	b.notify()
	return hist, ch, func() {
		b.tap.unsubscribe(ch)
		b.notify()
	}
}

// WriteDevice 直接向串口写入，供网页终端输入使用。
func (b *Bridge) WriteDevice(p []byte) error {
	b.mu.Lock()
	port := b.port
	b.mu.Unlock()
	if port == nil {
		if b.cfg.OpenOnConnect {
			return i18n.New("串口只在有客户端连接时打开")
		}
		return i18n.New("串口未打开")
	}
	n, err := port.Write(p)
	b.toDevice.Add(int64(n))
	if n > 0 {
		b.captureTx(p[:n])
		b.tap.publish(DirTXWeb, p[:n])
	}
	return err
}

// WriteClient 把网页终端的数据发给当前连接的 TCP 客户端，像是设备输出的一样（RFC2217 模式下照常转义）。
// 不计入串口流量，记入流量历史以便监看。
func (b *Bridge) WriteClient(p []byte) error {
	b.mu.Lock()
	c := b.client
	b.mu.Unlock()
	if c == nil {
		return i18n.New("没有已连接的 TCP 客户端")
	}
	n, err := c.Write(p)
	if n > 0 {
		b.tap.publish(DirRXWeb, p[:n])
	}
	if err != nil {
		b.dropClient(i18n.Sprintf("写入客户端失败: %v", err))
	}
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
	b.setStateLocked(StateWaitingDevice, nil)
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
		b.setStateLocked(next, nil)
		b.mu.Unlock()
		b.notify()
		return
	}
	cancel()
	for _, ln := range lns {
		_ = ln.Close()
	}
	b.dropClient(i18n.T("桥接停止"))
	b.closePort()
	b.wg.Wait()
	_ = b.StopCapture()
	b.mu.Lock()
	b.setStateLocked(next, nil)
	b.mu.Unlock()
	b.notify()
}

// kick 断开当前客户端。
func (b *Bridge) kick() bool {
	return b.dropClient(i18n.T("被管理员断开"))
}

func (b *Bridge) setStateLocked(s State, errMsg i18n.Text) {
	b.state = s
	if errMsg != nil {
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
		if b.cfg.OpenOnConnect && !b.hasClient() {
			// 没有客户端：不打开串口，等客户端连上
			b.mu.Lock()
			changed := b.state != StateListening
			b.lastErr = nil
			b.setStateLocked(StateListening, nil)
			b.mu.Unlock()
			if changed {
				b.notify()
			}
			select {
			case <-ctx.Done():
				return
			case <-b.wake:
			}
			continue
		}
		if err := b.openPort(); err != nil {
			msg := i18n.ErrText(err)
			if isNotFound(err) {
				msg = i18n.M("串口不存在，等待设备接入")
			} else if isBusy(err) {
				msg = i18n.M("串口被其他程序占用")
				if b.holder != nil {
					if other := b.holder(b.resolvedName(), b.cfg.ID); other != "" {
						msg = i18n.M("串口 %s 已被桥接 %s 使用（检查两个桥接的串口与 USB 比对设置）", b.resolvedName(), other)
					}
				}
			}
			b.mu.Lock()
			changed := b.state != StateWaitingDevice || i18n.String(b.lastErr) != msg.String()
			b.setStateLocked(StateWaitingDevice, msg)
			b.mu.Unlock()
			if changed {
				b.log.Warn(i18n.T("打开串口失败"), "err", err)
				b.notify()
			}
			if b.cfg.DisconnectOnUnplug {
				b.dropClient(i18n.T("串口无法打开"))
			}
			if !b.sleep(ctx) {
				return
			}
			continue
		}

		b.mu.Lock()
		b.lastErr = nil
		if b.client != nil {
			b.setStateLocked(StateConnected, nil)
		} else {
			b.setStateLocked(StateListening, nil)
		}
		b.mu.Unlock()
		b.log.Info(i18n.T("串口已打开"), "port", b.resolvedName(), "params", b.activeMode().String())
		b.notify()

		b.readPort(ctx)
		b.closePort()
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		idle := b.idleClosed
		b.idleClosed = false
		b.mu.Unlock()
		if idle {
			b.log.Info(i18n.T("客户端已断开，关闭串口"))
			continue
		}
		b.mu.Lock()
		b.setStateLocked(StateWaitingDevice, i18n.M("串口断开，等待重连"))
		b.mu.Unlock()
		b.log.Warn(i18n.T("串口断开，等待重连"))
		b.notify()
		if b.cfg.DisconnectOnUnplug {
			b.dropClient(i18n.T("串口已拔出"))
		}
		if !b.sleep(ctx) {
			return
		}
	}
}

// sleep 等待重试间隔；ctx 结束时返回 false。
func (b *Bridge) sleep(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(deviceRetryInterval):
		return true
	}
}

func (b *Bridge) hasClient() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client != nil
}

func (b *Bridge) openPort() error {
	var held func(string) bool
	if b.holder != nil {
		held = func(port string) bool { return b.holder(port, b.cfg.ID) != "" }
	}
	name, err := resolvePort(b.cfg, held)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.resolved = name // 打开失败时状态也显示实际尝试的串口
	mode := b.active
	dtr, rts := b.dtr, b.rts
	b.mu.Unlock()
	sm, err := serialMode(mode)
	if err != nil {
		return err
	}
	sm.InitialStatusBits = &serial.ModemOutputBits{DTR: dtr, RTS: rts}
	p, err := openSerial(name, sm)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.port = p
	b.resolved = name
	b.idleClosed = false
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

// readPort 把串口数据转发给客户端，并记入流量历史，直到出错或被关闭。
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
			b.captureRx(data)
			b.tap.publish(DirRX, data)
			b.mu.Lock()
			c := b.client
			b.mu.Unlock()
			if c != nil {
				if _, werr := c.Write(data); werr != nil {
					b.dropClient(i18n.Sprintf("写入客户端失败: %v", werr))
				}
			}
		}
		if err != nil {
			// Windows 上 SetCommState（客户端改波特率）会中止挂起的 ReadFile，库把它报成 PortClosed。
			// 串口句柄仍由我们持有且控制线还能读到，就当作瞬时错误继续读，不要把设备判成断开。
			var pe *serial.PortError
			if errors.As(err, &pe) && pe.Code() == serial.PortClosed && b.holdsPort(p) {
				if _, serr := p.GetModemStatusBits(); serr == nil {
					b.log.Debug(i18n.T("串口读取被中止后恢复"), "err", err)
					continue
				}
			}
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				b.log.Debug(i18n.T("串口读取结束"), "err", err)
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
		b.log.Info(i18n.T("客户端修改串口参数"), "params", m.String())
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
		return i18n.New("串口未打开")
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
		return i18n.New("串口未打开")
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
		return i18n.New("串口未打开")
	}
	return p.Break(d)
}

// Purge 清空缓冲区。
func (b *Bridge) Purge(rx, tx bool) error {
	b.mu.Lock()
	p := b.port
	b.mu.Unlock()
	if p == nil {
		return i18n.New("串口未打开")
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
		return rfc2217.ModemStatus{}, i18n.New("串口未打开")
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
					b.log.Debug(i18n.T("恢复配置参数失败"), "err", err)
				}
			}
		}
		if changedLines {
			_ = p.SetDTR(true)
			_ = p.SetRTS(true)
		}
	}
	b.log.Info(i18n.T("已恢复配置的串口参数"), "params", m.String())
	b.notify()
}

// ---- TCP 侧 ----

func (b *Bridge) listenLoop(ctx context.Context, listen ListenFunc, addr, kind string) {
	defer b.wg.Done()
	for ctx.Err() == nil {
		ln, err := listen("tcp", addr)
		if err != nil {
			b.mu.Lock()
			b.setStateLocked(StateError, i18n.M("%s 监听失败: %v", kind, err))
			b.mu.Unlock()
			b.log.Error(i18n.T("TCP 监听失败"), "kind", kind, "addr", addr, "err", err)
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
		b.log.Info(i18n.T("TCP 已监听"), "kind", kind, "addr", addr)

		for {
			c, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				b.log.Warn(i18n.T("接受连接失败"), "kind", kind, "err", err)
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
		label = i18n.Sprintf("本机 %s", remote)
	} else if b.whois != nil {
		wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if who := b.whois(wctx, remote); who != "" {
			label = who + " (" + remote + ")"
		}
		cancel()
	}

	b.mu.Lock()
	if b.cfg.DisconnectOnUnplug && !b.cfg.OpenOnConnect && b.port == nil {
		b.mu.Unlock()
		b.log.Info(i18n.T("串口不在，拒绝新连接"), "new", label)
		_, _ = c.Write([]byte("rsagent: device not connected\r\n"))
		_ = c.Close()
		return
	}
	if b.client != nil {
		if b.cfg.ClientPolicy == "reject" {
			b.mu.Unlock()
			b.log.Info(i18n.T("已有客户端，拒绝新连接"), "new", label)
			_, _ = c.Write([]byte("rsagent: port busy\r\n"))
			_ = c.Close()
			return
		}
		b.mu.Unlock()
		b.dropClient(i18n.T("被新连接替换"))
		b.mu.Lock()
	}
	tuneConn(c)

	var client io.ReadWriteCloser = c
	if b.cfg.Mode == "rfc2217" {
		srv := rfc2217.NewServer(c, b, b.log)
		srv.OnChange = b.notify
		b.mu.Unlock()
		if err := srv.Start(); err != nil {
			b.log.Warn(i18n.T("RFC2217 协商发送失败"), "err", err)
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
		b.setStateLocked(StateConnected, nil)
	}
	b.mu.Unlock()
	b.log.Info(i18n.T("客户端已连接"), "client", label, "mode", b.cfg.Mode)
	b.notify()
	if b.cfg.OpenOnConnect {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}

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
				if wn, werr := p.Write(buf[:n]); werr != nil {
					b.log.Debug(i18n.T("写入串口失败"), "err", werr)
				} else if wn > 0 {
					b.captureTx(buf[:wn])
					b.tap.publish(DirTX, buf[:wn])
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reason := i18n.T("客户端断开")
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				reason = i18n.T("空闲超时")
			} else if !errors.Is(err, io.EOF) {
				reason = i18n.Sprintf("读取客户端失败: %v", err)
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
		b.setStateLocked(StateListening, nil)
	}
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = c.Close()
	b.restoreConfigMode()
	b.log.Info(i18n.T("客户端已断开"), "client", label, "reason", reason)
	if b.cfg.OpenOnConnect {
		b.mu.Lock()
		open := b.port != nil && b.client == nil
		if open {
			b.idleClosed = true
		}
		b.mu.Unlock()
		if open {
			b.closePort() // deviceLoop 的读取随之结束，回到等待用户端
		}
	}
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
