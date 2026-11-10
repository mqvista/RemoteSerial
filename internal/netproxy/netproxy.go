// Package netproxy 实现前置代理：按设置让更新下载、套件安装与内嵌 Tailscale 节点经代理连网。
//
// 设置可以在运行中修改，所有使用方都在每次连接时读取当前设置：
//   - 更新：Updater 的 http.Transport.Proxy 用 ForUpdate。
//   - 套件管理器（opkg、apk 及其调用的 wget）：设置进程环境变量 http_proxy / https_proxy，子进程继承。
//     它们只支持 HTTP 代理。
//   - 内嵌 Tailscale（tsnet）：HTTP 代理经 tshttpproxy.SetProxyFunc；SOCKS5 经 netns 的 ALL_PROXY 拨号器。
//     两者都只在进程内读一次环境，所以 Install 在启动时挂上会读当前设置的函数。
//
// 只有 TCP 连接（控制服务器、DERP 中继）走代理；节点间直连用 UDP，代理无法转发，会改走中继。
package netproxy

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/proxy"
	"tailscale.com/net/tshttpproxy"

	"rsagent/internal/config"
)

// RedactedPassword 代替密码返回给界面；保存时原样传回表示沿用原密码。
const RedactedPassword = "******"

type state struct {
	u         *url.URL // nil 表示没有设置
	update    bool
	tailscale bool
}

var (
	cur atomic.Pointer[state]

	installOnce sync.Once
	envProxy    func(*url.URL) (*url.URL, error) // 启动时环境变量里的代理，未启用本功能时沿用

	envMu   sync.Mutex
	origEnv map[string]*string // 首次改动前的环境变量，关闭代理时恢复
)

func init() { cur.Store(&state{}) }

// Install 挂上 Tailscale 的代理钩子，并记下启动时环境变量里的代理。必须在启动内嵌节点前调用
// （Set 会调用），只生效一次。
func Install() {
	installOnce.Do(func() {
		envProxy = httpproxy.FromEnvironment().ProxyFunc()
		_ = tshttpproxy.SetProxyFunc(tailscaleHTTPProxy)

		// netns 用 x/net/proxy 读取 ALL_PROXY（只读一次）。用户自己设了 ALL_PROXY 时尊重它；
		// 否则临时设成自定义的 scheme 让它读到，之后每次拨号都由 dynDialer 按当前设置决定。
		if os.Getenv("ALL_PROXY") != "" || os.Getenv("all_proxy") != "" {
			return
		}
		proxy.RegisterDialerType(dynScheme, func(_ *url.URL, forward proxy.Dialer) (proxy.Dialer, error) {
			return &dynDialer{forward: forward}, nil
		})
		_ = os.Setenv("ALL_PROXY", dynScheme+"://settings")
		_ = proxy.FromEnvironment()
		_ = os.Unsetenv("ALL_PROXY")
	})
}

const dynScheme = "rsagent-proxy"

// Set 应用设置。地址非法时视为没有代理（配置校验会先拦下）。
func Set(p config.Proxy) {
	Install()
	st := &state{update: p.Update, tailscale: p.Tailscale}
	if p.URL != "" {
		if u, err := config.ParseProxyURL(p.URL); err == nil {
			st.u = u
		}
	}
	cur.Store(st)
	applyEnv(st)
}

// IsHTTP 判断代理是否为 HTTP(S) 代理（套件管理器只支持这种）。
func IsHTTP(u *url.URL) bool { return u != nil && (u.Scheme == "http" || u.Scheme == "https") }

// ForUpdate 是更新下载用的 http.Transport.Proxy。
func ForUpdate(req *http.Request) (*url.URL, error) {
	Install()
	st := cur.Load()
	if st.update && st.u != nil {
		if bypass(req.URL.Hostname()) {
			return nil, nil
		}
		return st.u, nil
	}
	// 不用 http.ProxyFromEnvironment：它只读一次环境，可能读到 applyEnv 设的值
	if envProxy == nil {
		return nil, nil
	}
	return envProxy(req.URL)
}

// tailscaleHTTPProxy 供 tshttpproxy 使用：控制服务器与 DERP 的 HTTP CONNECT 代理。
// SOCKS5 不在这里返回（DERP 只会 CONNECT），由 dynDialer 处理。
func tailscaleHTTPProxy(target *url.URL) (*url.URL, error) {
	st := cur.Load()
	if st.tailscale && st.u != nil {
		if !IsHTTP(st.u) || bypass(target.Hostname()) {
			return nil, nil
		}
		return st.u, nil
	}
	if envProxy == nil {
		return nil, nil
	}
	return envProxy(target)
}

// dynDialer 是 netns 拨号时套上的一层：启用了 Tailscale 的 SOCKS5 代理时经代理拨号，否则直连。
//
// 连到代理本身时不用 netns 的拨号器：它在 macOS 上把连线绑在默认网卡上，代理在别的网卡
// （第二张网卡、VPN、虚拟机网络）时会连不上。HTTP 代理由 Tailscale 自己拨号，也经过这里。
type dynDialer struct{ forward proxy.Dialer }

var plainDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

func (d *dynDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *dynDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	st := cur.Load()
	if st.tailscale && st.u != nil {
		if addr == proxyAddr(st.u) {
			return plainDialer.DialContext(ctx, network, addr)
		}
		host, _, _ := net.SplitHostPort(addr)
		if !IsHTTP(st.u) && !bypass(host) {
			var auth *proxy.Auth
			if st.u.User != nil {
				pw, _ := st.u.User.Password()
				auth = &proxy.Auth{User: st.u.User.Username(), Password: pw}
			}
			sd, err := proxy.SOCKS5("tcp", proxyAddr(st.u), auth, plainDialer)
			if err != nil {
				return nil, err
			}
			return sd.(proxy.ContextDialer).DialContext(ctx, network, addr)
		}
	}
	if cd, ok := d.forward.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}
	return d.forward.Dial(network, addr)
}

// proxyAddr 返回代理的 主机:端口，省略端口时按类型补上默认值。
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "1080"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// bypass 判断目标是否不走代理：本机、局域网与链路本地地址（如局域网内的 Headscale）。
func bypass(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

var envNames = []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"}

// applyEnv 让子进程（opkg、apk、wget）使用更新代理；关闭时恢复启动时的环境变量。
func applyEnv(st *state) {
	envMu.Lock()
	defer envMu.Unlock()
	if origEnv == nil {
		origEnv = map[string]*string{}
		for _, n := range envNames {
			if v, ok := os.LookupEnv(n); ok {
				origEnv[n] = &v
			} else {
				origEnv[n] = nil
			}
		}
	}
	if st.update && IsHTTP(st.u) {
		for _, n := range envNames {
			_ = os.Setenv(n, st.u.String())
		}
		return
	}
	for _, n := range envNames {
		if v := origEnv[n]; v != nil {
			_ = os.Setenv(n, *v)
		} else {
			_ = os.Unsetenv(n)
		}
	}
}

// Redact 把地址里的密码换成 RedactedPassword，供界面显示。
func Redact(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	if _, ok := u.User.Password(); !ok {
		return s
	}
	// 不用 url.UserPassword：它会把 * 编码成 %2A
	user := url.User(u.User.Username()).String()
	u.User = nil
	rest := strings.TrimPrefix(u.String(), u.Scheme+"://")
	return u.Scheme + "://" + user + ":" + RedactedPassword + "@" + rest
}

// Scrub 把日志中出现的当前代理密码换成 RedactedPassword（含 Base64 的 Proxy-Authorization）。
func Scrub(msg string) string {
	st := cur.Load()
	if st.u == nil || st.u.User == nil {
		return msg
	}
	pw, ok := st.u.User.Password()
	if !ok || pw == "" || !strings.Contains(msg, "@") && !strings.Contains(msg, "Basic ") {
		return msg
	}
	user := st.u.User.Username()
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + pw))
	msg = strings.ReplaceAll(msg, basic, RedactedPassword)
	for _, enc := range []string{url.UserPassword(user, pw).String(), user + ":" + pw} {
		msg = strings.ReplaceAll(msg, enc+"@", url.User(user).String()+":"+RedactedPassword+"@")
	}
	return msg
}

// Restore 在界面传回的地址密码为 RedactedPassword 且用户名、主机未变时（端口、类型可以改），换回 old 中的原密码。
func Restore(s, old string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.User == nil {
		return strings.TrimSpace(s)
	}
	pw, ok := u.User.Password()
	if !ok || pw != RedactedPassword {
		return strings.TrimSpace(s)
	}
	o, err := url.Parse(old)
	if err != nil || o.User == nil || o.User.Username() != u.User.Username() || !strings.EqualFold(o.Hostname(), u.Hostname()) {
		return strings.TrimSpace(s)
	}
	opw, _ := o.User.Password()
	u.User = url.UserPassword(u.User.Username(), opw)
	return u.String()
}
