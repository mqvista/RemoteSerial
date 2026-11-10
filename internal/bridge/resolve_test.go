package bridge

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"rsagent/internal/config"
)

func TestResolvePortPrefersFree(t *testing.T) {
	old := listPortsFn
	defer func() { listPortsFn = old }()
	listPortsFn = func() ([]PortInfo, error) {
		return []PortInfo{
			{Name: "/dev/a", IsUSB: true, VID: "303A", PID: "1001"},
			{Name: "/dev/b", IsUSB: true, VID: "303A", PID: "1001"},
		}, nil
	}
	cfg := config.Bridge{Port: "/dev/a", Match: &config.PortMatch{VID: "303A", PID: "1001"}}
	if got, _ := resolvePort(cfg, nil); got != "/dev/a" {
		t.Fatalf("no holder: got %s", got)
	}
	held := func(p string) bool { return p == "/dev/a" }
	if got, _ := resolvePort(cfg, held); got != "/dev/b" {
		t.Fatalf("a held: got %s, want /dev/b", got)
	}
	all := func(string) bool { return true }
	if got, _ := resolvePort(cfg, all); got != "/dev/a" {
		t.Fatalf("all held: got %s, want configured /dev/a", got)
	}
}

func TestHolderOf(t *testing.T) {
	dev := &fakeDevice{present: true}
	old := openSerial
	openSerial = dev.openFn
	defer func() { openSerial = old }()
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	b1 := config.Bridge{ID: "b1", Name: "Node01", Port: "/dev/x", Mode: "raw", Baud: 9600, DataBits: 8, Parity: "N", StopBits: 1, ClientPolicy: "replace", TCPPort: 1, Enabled: true}
	m.Apply([]config.Bridge{b1})
	m.SetListener(func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") })
	defer m.StopAll()
	waitFor(t, "b1 open", dev.isOpen)
	waitFor(t, "b1 status", func() bool { return m.holderOf("/dev/x", "b2") == "Node01" })
	if got := m.holderOf("/dev/x", "b1"); got != "" {
		t.Fatalf("self must be skipped, got %q", got)
	}
	if got := m.holderOf("/dev/y", "b2"); got != "" {
		t.Fatalf("other port: got %q", got)
	}
}
