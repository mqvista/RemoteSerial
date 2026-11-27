package tsnode

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"tailscale.com/types/nettype"
	"tailscale.com/wgengine/netstack"
)

func TestShouldForwardSubnet(t *testing.T) {
	n := New(Config{
		AdvertiseRoutes: []string{"192.168.0.0/24", "10.20.0.0/16"},
	}, nil, Hooks{})

	cases := []struct {
		ip   string
		want bool
	}{
		{"192.168.0.1", true},
		{"192.168.0.123", true},
		{"::ffff:192.168.0.1", true},
		{"10.20.30.40", true},
		{"192.168.1.1", false},
		{"100.67.209.69", false},
		{"100.100.100.100", false},
		{"fd7a:115c:a1e0::3c37:d146", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"0.0.0.0", false},
	}
	for _, tc := range cases {
		if got := n.shouldForwardSubnet(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("shouldForwardSubnet(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}

	// 动态更新子网路由后立即生效（包括 0.0.0.0/0 仍不放行 Tailscale IP 与回环地址）
	if err := n.SetAdvertiseRoutesConfig(context.Background(), []string{"0.0.0.0/0"}); err != nil {
		t.Fatal(err)
	}
	if !n.shouldForwardSubnet(netip.MustParseAddr("192.168.99.1")) {
		t.Error("expected 192.168.99.1 forwarded under 0.0.0.0/0")
	}
	for _, blocked := range []string{"100.67.209.69", "100.100.100.100", "127.0.0.1", "0.0.0.0"} {
		if n.shouldForwardSubnet(netip.MustParseAddr(blocked)) {
			t.Errorf("expected %s blocked even under 0.0.0.0/0", blocked)
		}
	}

	if err := n.SetAdvertiseRoutesConfig(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n.shouldForwardSubnet(netip.MustParseAddr("192.168.0.1")) {
		t.Error("expected 192.168.0.1 blocked after clearing routes")
	}
}

func TestWrapNetstackHandlers(t *testing.T) {
	n := New(Config{AdvertiseRoutes: []string{"192.168.0.0/24"}}, nil, Hooks{})

	tcpFallbackCalls := 0
	udpFallbackCalls := 0
	var dummyTCPHandler = func(net.Conn) {}
	var dummyUDPHandler = func(nettype.ConnPacketConn) {}

	ns := &netstack.Impl{
		GetTCPHandlerForFlow: func(src, dst netip.AddrPort) (func(net.Conn), bool) {
			tcpFallbackCalls++
			if dst.Port() == 80 {
				return dummyTCPHandler, true
			}
			return nil, true // tsnet 默认：未监听端口直接拒绝
		},
		GetUDPHandlerForFlow: func(src, dst netip.AddrPort) (func(nettype.ConnPacketConn), bool) {
			udpFallbackCalls++
			if dst.Port() == 5353 {
				return dummyUDPHandler, true
			}
			return nil, true
		},
	}
	n.wrapNetstackHandlers(ns)

	src := netip.MustParseAddrPort("100.77.38.126:50000")

	// 子网目标：不拦截，交给 netstack forwardTCP / forwardUDP
	if h, intercept := ns.GetTCPHandlerForFlow(src, netip.MustParseAddrPort("192.168.0.1:502")); h != nil || intercept {
		t.Fatalf("subnet TCP: got (handler=%v, intercept=%v), want (false, false)", h != nil, intercept)
	}
	if h, intercept := ns.GetUDPHandlerForFlow(src, netip.MustParseAddrPort("192.168.0.1:161")); h != nil || intercept {
		t.Fatalf("subnet UDP: got (handler=%v, intercept=%v), want (false, false)", h != nil, intercept)
	}
	if tcpFallbackCalls != 0 || udpFallbackCalls != 0 {
		t.Fatalf("subnet traffic should bypass tsnet handlers, got calls tcp=%d udp=%d", tcpFallbackCalls, udpFallbackCalls)
	}

	// 本节点 Tailscale IP 已监听端口：走原 tsnet handler
	if h, intercept := ns.GetTCPHandlerForFlow(src, netip.MustParseAddrPort("100.67.209.69:80")); h == nil || !intercept {
		t.Fatalf("tailnet served TCP: got (handler=%v, intercept=%v), want (true, true)", h != nil, intercept)
	}
	if h, intercept := ns.GetUDPHandlerForFlow(src, netip.MustParseAddrPort("100.67.209.69:5353")); h == nil || !intercept {
		t.Fatalf("tailnet served UDP: got (handler=%v, intercept=%v), want (true, true)", h != nil, intercept)
	}

	// 本节点 Tailscale IP 未监听端口：仍由 tsnet 拒绝，不落入 localhost 转发
	if h, intercept := ns.GetTCPHandlerForFlow(src, netip.MustParseAddrPort("100.67.209.69:7890")); h != nil || !intercept {
		t.Fatalf("tailnet unserved TCP: got (handler=%v, intercept=%v), want (false, true)", h != nil, intercept)
	}
	if h, intercept := ns.GetUDPHandlerForFlow(src, netip.MustParseAddrPort("100.67.209.69:9999")); h != nil || !intercept {
		t.Fatalf("tailnet unserved UDP: got (handler=%v, intercept=%v), want (false, true)", h != nil, intercept)
	}
}
