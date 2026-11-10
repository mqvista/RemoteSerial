package netproxy

import (
	"net/http"
	"net/url"
	"testing"

	"tailscale.com/net/netmon"
	"tailscale.com/net/netns"
	"tailscale.com/net/tshttpproxy"
	"tailscale.com/util/eventbus"

	"rsagent/internal/config"
)

// 确认钩子真的挂进了 Tailscale：tshttpproxy 返回 HTTP 代理，netns 的拨号器套上了 dynDialer。
func TestTailscaleHooks(t *testing.T) {
	Set(config.Proxy{URL: "http://p:3128", Tailscale: true})
	defer Set(config.Proxy{})
	u, _ := url.Parse("https://controlplane.tailscale.com/key")
	got, err := tshttpproxy.ProxyFromEnvironment(&http.Request{URL: u})
	if err != nil || got == nil || got.Host != "p:3128" {
		t.Fatalf("tshttpproxy = %v, %v", got, err)
	}
	mon, err := netmon.New(eventbus.New(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	d := netns.NewDialer(t.Logf, mon)
	if _, ok := d.(*dynDialer); !ok {
		t.Fatalf("netns dialer is %T, want *dynDialer", d)
	}
}
