package bridge

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"

	"rsagent/internal/config"
)

// fakeDevice 模拟一个可插拔的串口：unplug 后打开失败，已打开的端口读取出错。
type fakeDevice struct {
	mu      sync.Mutex
	present bool
	opens   int
	open    *fakePort
}

type fakePort struct {
	serial.Port // 未用到的方法
	closed      chan struct{}
	once        sync.Once
}

func (p *fakePort) Read(b []byte) (int, error) {
	<-p.closed
	return 0, errors.New("port gone")
}
func (p *fakePort) Write(b []byte) (int, error) { return len(b), nil }
func (p *fakePort) SetMode(*serial.Mode) error  { return nil }
func (p *fakePort) SetDTR(bool) error           { return nil }
func (p *fakePort) SetRTS(bool) error           { return nil }
func (p *fakePort) Close() error                { p.once.Do(func() { close(p.closed) }); return nil }

func (d *fakeDevice) openFn(string, *serial.Mode) (serial.Port, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.present {
		return nil, errors.New("no such file or directory")
	}
	d.opens++
	d.open = &fakePort{closed: make(chan struct{})}
	return d.open, nil
}

func (d *fakeDevice) unplug() {
	d.mu.Lock()
	d.present = false
	p := d.open
	d.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

func (d *fakeDevice) isOpen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open == nil {
		return false
	}
	select {
	case <-d.open.closed:
		return false
	default:
		return true
	}
}

func startTestBridge(t *testing.T, cfg config.Bridge, dev *fakeDevice) (*Bridge, string) {
	t.Helper()
	old := openSerial
	openSerial = dev.openFn
	t.Cleanup(func() { openSerial = old })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg.ID, cfg.Port, cfg.Mode, cfg.Baud, cfg.DataBits, cfg.Parity, cfg.StopBits, cfg.ClientPolicy = "t", "/dev/fake", "raw", 9600, 8, "N", 1, "replace"
	b := newBridge(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	b.start(func(network, _ string) (net.Listener, error) { return net.Listen(network, addr) })
	t.Cleanup(func() { b.stop(StateDisabled) })
	return b, addr
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	var c net.Conn
	var err error
	waitFor(t, "listener", func() bool { c, err = net.Dial("tcp", addr); return err == nil })
	return c
}

// closedByPeer 判断连接是否被对方关闭（读到 EOF 或其他错误）。
func closedByPeer(c net.Conn) bool {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	for {
		_, err := c.Read(buf)
		if err != nil {
			var ne net.Error
			return !(errors.As(err, &ne) && ne.Timeout())
		}
	}
}

func TestDefaultKeepsClientOnUnplug(t *testing.T) {
	dev := &fakeDevice{present: true}
	b, addr := startTestBridge(t, config.Bridge{}, dev)
	waitFor(t, "port open", dev.isOpen)
	c := dial(t, addr)
	defer c.Close()
	waitFor(t, "client", b.hasClient)
	dev.unplug()
	waitFor(t, "waiting device", func() bool { return b.Status().State == StateWaitingDevice })
	time.Sleep(200 * time.Millisecond)
	if !b.hasClient() {
		t.Fatal("client dropped without disconnectOnUnplug")
	}
}

func TestDisconnectOnUnplug(t *testing.T) {
	dev := &fakeDevice{present: true}
	b, addr := startTestBridge(t, config.Bridge{DisconnectOnUnplug: true}, dev)
	waitFor(t, "port open", dev.isOpen)
	c := dial(t, addr)
	defer c.Close()
	waitFor(t, "client", b.hasClient)
	dev.unplug()
	if !closedByPeer(c) {
		t.Fatal("client not disconnected on unplug")
	}
	// 设备不在：新连接被拒绝
	c2 := dial(t, addr)
	defer c2.Close()
	if !closedByPeer(c2) {
		t.Fatal("new connection accepted while the device is missing")
	}
}

func TestOpenOnConnect(t *testing.T) {
	dev := &fakeDevice{present: true}
	b, addr := startTestBridge(t, config.Bridge{OpenOnConnect: true}, dev)
	time.Sleep(300 * time.Millisecond)
	if dev.isOpen() {
		t.Fatal("port opened without a client")
	}
	if s := b.Status().State; s != StateListening {
		t.Fatalf("state %s, want Listening", s)
	}
	c := dial(t, addr)
	waitFor(t, "port open on connect", dev.isOpen)
	waitFor(t, "connected", func() bool { return b.Status().State == StateConnected })
	c.Close()
	waitFor(t, "port closed after disconnect", func() bool { return !dev.isOpen() })
	waitFor(t, "listening", func() bool { return b.Status().State == StateListening })
	// 再连一次：重新打开
	c = dial(t, addr)
	defer c.Close()
	waitFor(t, "port reopened", dev.isOpen)
	if dev.opens != 2 {
		t.Fatalf("opens = %d, want 2", dev.opens)
	}
}

func TestOpenOnConnectWithDisconnect(t *testing.T) {
	dev := &fakeDevice{present: false}
	_, addr := startTestBridge(t, config.Bridge{OpenOnConnect: true, DisconnectOnUnplug: true}, dev)
	c := dial(t, addr)
	defer c.Close()
	if !closedByPeer(c) {
		t.Fatal("client kept although the device cannot be opened")
	}
}
