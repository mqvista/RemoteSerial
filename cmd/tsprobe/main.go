// tsprobe 用一个临时 tsnet 节点访问 tailnet 内的地址，用于联调 rsagent 的
// tailnet 侧鉴权和桥接端口，不需要本机安装官方客户端。
//
//	tsprobe -authkey <key> -get http://100.x.y.z/api/status
//	tsprobe -authkey <key> -tcp 100.x.y.z:2217 -send "hello\r\n"
//	tsprobe -authkey <key> -ping 100.x.y.z
//
// 节点为 ephemeral，退出后自动从 tailnet 消失；状态放在临时目录。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

func main() {
	authKey := flag.String("authkey", "", "auth key，留空则读环境变量 TS_AUTHKEY")
	hostname := flag.String("hostname", "rs-probe", "探测节点名")
	getURL := flag.String("get", "", "通过 tailnet 发 HTTP GET")
	tcpAddr := flag.String("tcp", "", "通过 tailnet 连接 TCP 地址")
	pingIP := flag.String("ping", "", "对目标 tailnet IP 做 disco 和 TSMP ping")
	send := flag.String("send", "", "TCP 连接后发送的内容，支持 \\r\\n 转义")
	wait := flag.Duration("wait", 3*time.Second, "TCP 发送后等待回包的时间")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP 与 TCP 连接超时")
	stateDir := flag.String("state", "", "状态目录，默认临时目录")
	flag.Parse()

	if *authKey == "" {
		*authKey = os.Getenv("TS_AUTHKEY")
	}
	if *getURL == "" && *tcpAddr == "" && *pingIP == "" {
		fmt.Fprintln(os.Stderr, "需要 -get、-tcp 或 -ping")
		os.Exit(2)
	}
	dir := *stateDir
	if dir == "" {
		d, err := os.MkdirTemp("", "tsprobe-")
		if err != nil {
			fatal(err)
		}
		defer os.RemoveAll(d)
		dir = d
	}

	srv := &tsnet.Server{
		Dir:       filepath.Clean(dir),
		Hostname:  *hostname,
		AuthKey:   *authKey,
		Ephemeral: true,
		Logf:      func(string, ...any) {},
		UserLogf:  func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[tsnet] "+f+"\n", a...) },
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := srv.Up(ctx)
	if err != nil {
		fatal(fmt.Errorf("上线失败: %w", err))
	}
	fmt.Fprintf(os.Stderr, "[probe] 已上线 %s %v\n", st.Self.DNSName, st.TailscaleIPs)
	lc, _ := srv.LocalClient()

	target := *pingIP
	if target == "" {
		if h := hostOf(*getURL, *tcpAddr); h != "" {
			target = h
		}
	}
	if target != "" {
		ip, err := netip.ParseAddr(target)
		if err == nil {
			for _, pt := range []tailcfg.PingType{tailcfg.PingDisco, tailcfg.PingTSMP} {
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				res, err := lc.Ping(pctx, ip, pt)
				pcancel()
				if err != nil {
					fmt.Fprintf(os.Stderr, "[probe] %s ping %s 失败: %v\n", pt, ip, err)
					continue
				}
				fmt.Fprintf(os.Stderr, "[probe] %s ping %s: %.1fms endpoint=%s derp=%s err=%s\n", pt, ip, res.LatencySeconds*1000, res.Endpoint, res.DERPRegionCode, res.Err)
			}
			printPeer(ctx, lc, ip)
		}
	}

	if *getURL != "" {
		hc := srv.HTTPClient()
		hc.Timeout = *timeout
		resp, err := hc.Get(*getURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: GET 失败: %v\n", err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Printf("HTTP %d\n%s\n", resp.StatusCode, body)
		}
	}

	if *tcpAddr != "" {
		dctx, dcancel := context.WithTimeout(ctx, *timeout)
		defer dcancel()
		c, err := srv.Dial(dctx, "tcp", *tcpAddr)
		if err != nil {
			fatal(fmt.Errorf("TCP 连接失败: %w", err))
		}
		defer c.Close()
		fmt.Fprintf(os.Stderr, "[probe] TCP 已连接 %s\n", *tcpAddr)
		if *send != "" {
			payload := strings.NewReplacer(`\r`, "\r", `\n`, "\n").Replace(*send)
			if _, err := c.Write([]byte(payload)); err != nil {
				fatal(err)
			}
		}
		_ = c.SetReadDeadline(time.Now().Add(*wait))
		buf := make([]byte, 4096)
		total := 0
		for {
			n, err := c.Read(buf)
			if n > 0 {
				total += n
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				var ne net.Error
				if !(errors.As(err, &ne) && ne.Timeout()) && !errors.Is(err, io.EOF) {
					fmt.Fprintf(os.Stderr, "[probe] 读取结束: %v\n", err)
				}
				break
			}
		}
		fmt.Fprintf(os.Stderr, "\n[probe] 共收到 %d 字节\n", total)
	}
}

func hostOf(getURL, tcpAddr string) string {
	if tcpAddr != "" {
		h, _, err := net.SplitHostPort(tcpAddr)
		if err == nil {
			return h
		}
		return tcpAddr
	}
	if getURL != "" {
		u := strings.TrimPrefix(strings.TrimPrefix(getURL, "http://"), "https://")
		if i := strings.IndexAny(u, "/"); i >= 0 {
			u = u[:i]
		}
		if h, _, err := net.SplitHostPort(u); err == nil {
			return h
		}
		return u
	}
	return ""
}

func printPeer(ctx context.Context, lc *local.Client, ip netip.Addr) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := lc.Status(sctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[probe] 读取状态失败: %v\n", err)
		return
	}
	for _, p := range st.Peer {
		for _, pip := range p.TailscaleIPs {
			if pip == ip {
				fmt.Fprintf(os.Stderr, "[probe] 对端 %s online=%v curAddr=%q relay=%q lastHandshake=%s\n",
					strings.TrimSuffix(p.DNSName, "."), p.Online, p.CurAddr, p.Relay, p.LastHandshake.Format(time.RFC3339))
				return
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[probe] 对端 %s 不在 netmap 中，ACL 可能不允许本节点访问它\n", ip)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
