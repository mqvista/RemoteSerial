package tsnode

import (
	"net"
	"net/netip"
	"strings"

	"tailscale.com/net/tsaddr"
	"tailscale.com/tsnet"
	"tailscale.com/types/nettype"
	"tailscale.com/wgengine/netstack"
)

// parseValidPrefixes 解析配置里的合法 CIDR 列表，用于子网转发判定。
func parseValidPrefixes(routes []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(routes))
	for _, r := range routes {
		if p, err := netip.ParsePrefix(strings.TrimSpace(r)); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// shouldForwardSubnet 判断目标 IP 是否应由 netstack 转发到本机可达的子网。
// 节点自身的 Tailscale IP、回环与未指定地址一律不放行，避免未监听端口泄漏到 localhost。
func (n *Node) shouldForwardSubnet(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() {
		return false
	}
	if tsaddr.IsTailscaleIP(ip) && !tsaddr.TailscaleViaRange().Contains(ip) {
		return false
	}
	n.mu.Lock()
	prefixes := n.routePrefixes
	n.mu.Unlock()
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// enableSubnetForwarding 绕过 tsnet 默认对未监听流量的全拦截，使已宣告子网的 TCP 与 UDP
// 流量能够落入 netstack 原生的 forwardTCP / forwardUDP 进行转发。
//
// tsnet.Server.start() 把 netstack.GetTCPHandlerForFlow / GetUDPHandlerForFlow 设为在找不到
// tsnet.Listen 监听器时返回 (nil, true)，原意是防止节点自身未监听的端口被转发到 localhost；
// 但这也会把子网路由目标（如 192.168.0.1）在进入 forwardTCP / forwardUDP 之前直接 RST / 丢弃。
func (n *Node) enableSubnetForwarding(srv *tsnet.Server) {
	sys := srv.Sys()
	if sys == nil {
		return
	}
	nsImpl, ok := sys.Netstack.GetOK()
	if !ok {
		return
	}
	ns, ok := nsImpl.(*netstack.Impl)
	if !ok || ns == nil {
		return
	}
	n.wrapNetstackHandlers(ns)
}

func (n *Node) wrapNetstackHandlers(ns *netstack.Impl) {
	origTCP := ns.GetTCPHandlerForFlow
	ns.GetTCPHandlerForFlow = func(src, dst netip.AddrPort) (func(net.Conn), bool) {
		if n.shouldForwardSubnet(dst.Addr()) {
			return nil, false
		}
		if origTCP != nil {
			return origTCP(src, dst)
		}
		return nil, false
	}

	origUDP := ns.GetUDPHandlerForFlow
	ns.GetUDPHandlerForFlow = func(src, dst netip.AddrPort) (func(nettype.ConnPacketConn), bool) {
		if n.shouldForwardSubnet(dst.Addr()) {
			return nil, false
		}
		if origUDP != nil {
			return origUDP(src, dst)
		}
		return nil, false
	}
}
