package tsnode

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"tailscale.com/net/netcheck"
	"tailscale.com/tailcfg"
)

// PingResult 是一次 ping 的结果。Type 为 disco（打洞层）或 TSMP（WireGuard 隧道内）。
type PingResult struct {
	Type      string  `json:"type"`
	OK        bool    `json:"ok"`
	LatencyMs float64 `json:"latencyMs"`
	Endpoint  string  `json:"endpoint,omitempty"`  // 直连时的对端地址
	DERP      string  `json:"derp,omitempty"`      // 中转时的 DERP 区域代码
	PeerRelay string  `json:"peerRelay,omitempty"` // 经 peer relay 时的地址
	NodeName  string  `json:"nodeName,omitempty"`
	Err       string  `json:"err,omitempty"`
}

// Ping 对一个 tailnet IP 做 disco 和 TSMP ping，用于判断直连还是中转。
func (n *Node) Ping(ctx context.Context, ipStr string) ([]PingResult, error) {
	n.mu.Lock()
	lc := n.lc
	n.mu.Unlock()
	if lc == nil {
		return nil, errors.New("节点未启动")
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil {
		return nil, fmt.Errorf("IP 非法: %w", err)
	}
	out := make([]PingResult, 0, 2)
	for _, pt := range []tailcfg.PingType{tailcfg.PingDisco, tailcfg.PingTSMP} {
		res := PingResult{Type: string(pt)}
		pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		r, err := lc.Ping(pctx, ip, pt)
		cancel()
		if err != nil {
			res.Err = err.Error()
		} else if r.Err != "" {
			res.Err = r.Err
		} else {
			res.OK = true
			res.LatencyMs = r.LatencySeconds * 1000
			res.Endpoint = r.Endpoint
			res.DERP = r.DERPRegionCode
			res.PeerRelay = r.PeerRelay
			res.NodeName = r.NodeName
		}
		out = append(out, res)
	}
	return out, nil
}

// RegionLatency 是到一个 DERP 区域的延迟。
type RegionLatency struct {
	ID        int     `json:"id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	LatencyMs float64 `json:"latencyMs"`
	Preferred bool    `json:"preferred"`
}

// NetcheckReport 是网络环境检测结果，对应 tailscale netcheck。
type NetcheckReport struct {
	UDP           bool            `json:"udp"`
	IPv4          bool            `json:"ipv4"`
	IPv6          bool            `json:"ipv6"`
	HardNAT       *bool           `json:"hardNAT,omitempty"` // 映射随目标变化，打洞困难
	UPnP          *bool           `json:"upnp,omitempty"`
	PMP           *bool           `json:"pmp,omitempty"`
	PCP           *bool           `json:"pcp,omitempty"`
	CaptivePortal *bool           `json:"captivePortal,omitempty"`
	GlobalV4      string          `json:"globalV4,omitempty"`
	GlobalV6      string          `json:"globalV6,omitempty"`
	PreferredDERP string          `json:"preferredDERP,omitempty"`
	Regions       []RegionLatency `json:"regions"`
	TookMs        int64           `json:"tookMs"`
	ReportAt      time.Time       `json:"reportAt"`
}

// Netcheck 触发节点自身的 magicsock 重新探测，并返回它的最新 netcheck 报告。
//
// 用节点自己的报告而不是另起一个探测客户端，结果才和真实打洞路径一致。
func (n *Node) Netcheck(ctx context.Context) (*NetcheckReport, error) {
	n.mu.Lock()
	srv := n.srv
	lc := n.lc
	n.mu.Unlock()
	if srv == nil || lc == nil {
		return nil, errors.New("节点未启动")
	}
	dm, err := lc.CurrentDERPMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 DERP 列表: %w", err)
	}
	sys := srv.Sys()
	if sys == nil {
		return nil, errors.New("节点内部状态不可用")
	}
	ms := sys.MagicSock.Get()
	if ms == nil {
		return nil, errors.New("magicsock 不可用")
	}
	start := time.Now()
	var prev time.Time
	if last := ms.GetLastNetcheckReport(ctx); last != nil {
		prev = last.Now
	}
	ms.ReSTUN("rsagent-diag")

	// 等待新报告，最多 12 秒；超时则退回最近一次报告。
	var rep *netcheck.Report
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if r := ms.GetLastNetcheckReport(ctx); r != nil && r.Now.After(prev) {
			rep = r
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	if rep == nil {
		rep = ms.GetLastNetcheckReport(ctx)
		if rep == nil {
			return nil, errors.New("暂无 netcheck 报告，请稍后再试")
		}
	}
	out := &NetcheckReport{
		UDP:      rep.UDP,
		IPv4:     rep.IPv4,
		IPv6:     rep.IPv6,
		HardNAT:  optBool(rep.MappingVariesByDestIP.Get()),
		UPnP:     optBool(rep.UPnP.Get()),
		PMP:      optBool(rep.PMP.Get()),
		PCP:      optBool(rep.PCP.Get()),
		TookMs:   time.Since(start).Milliseconds(),
		Regions:  []RegionLatency{},
		ReportAt: rep.Now,
	}
	if v, ok := rep.CaptivePortal.Get(); ok {
		out.CaptivePortal = &v
	}
	if rep.GlobalV4.IsValid() {
		out.GlobalV4 = rep.GlobalV4.String()
	}
	if rep.GlobalV6.IsValid() {
		out.GlobalV6 = rep.GlobalV6.String()
	}
	for id, d := range rep.RegionLatency {
		rl := RegionLatency{ID: int(id), LatencyMs: float64(d.Microseconds()) / 1000, Preferred: id == rep.PreferredDERP}
		if dm != nil {
			if r, ok := dm.Regions[id]; ok && r != nil {
				rl.Code = r.RegionCode
				rl.Name = r.RegionName
			}
		}
		out.Regions = append(out.Regions, rl)
	}
	sort.Slice(out.Regions, func(i, j int) bool { return out.Regions[i].LatencyMs < out.Regions[j].LatencyMs })
	if rep.PreferredDERP != 0 && dm != nil {
		if r, ok := dm.Regions[rep.PreferredDERP]; ok && r != nil {
			out.PreferredDERP = fmt.Sprintf("%s (%s)", r.RegionName, r.RegionCode)
		}
	}
	return out, nil
}

func optBool(v, ok bool) *bool {
	if !ok {
		return nil
	}
	return &v
}
