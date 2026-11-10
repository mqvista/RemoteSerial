package netproxy

import (
	"net/http"
	"net/url"
	"testing"

	"rsagent/internal/config"
)

func TestRedactRestore(t *testing.T) {
	orig := "http://alice:s3cret@proxy.example:8080"
	shown := Redact(orig)
	if shown != "http://alice:******@proxy.example:8080" {
		t.Fatalf("Redact = %q", shown)
	}
	if got := Restore(shown, orig); got != orig {
		t.Fatalf("Restore = %q, want %q", got, orig)
	}
	// 改了主机：不沿用旧密码
	if got := Restore("http://alice:******@other:8080", orig); got == orig || got == "http://alice:s3cret@other:8080" {
		t.Fatalf("Restore with new host = %q", got)
	}
	// 只改端口与类型：沿用
	if got := Restore("socks5://alice:******@proxy.example:1080", orig); got != "socks5://alice:s3cret@proxy.example:1080" {
		t.Fatalf("Restore with new port = %q", got)
	}
	if got := Restore("http://alice:new@proxy.example:8080", orig); got != "http://alice:new@proxy.example:8080" {
		t.Fatalf("Restore with new password = %q", got)
	}
	if Redact("socks5://h:1080") != "socks5://h:1080" {
		t.Fatal("Redact without user changed the URL")
	}
}

func TestForUpdateAndTailscale(t *testing.T) {
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	Set(config.Proxy{URL: "http://p:3128", Update: true})
	if u, _ := ForUpdate(req("https://github.com/x")); u == nil || u.Host != "p:3128" {
		t.Fatalf("ForUpdate = %v", u)
	}
	if u, _ := ForUpdate(req("http://192.168.1.10/m.json")); u != nil {
		t.Fatalf("LAN address should bypass, got %v", u)
	}
	target, _ := url.Parse("https://controlplane.tailscale.com")
	if u, _ := tailscaleHTTPProxy(target); u != nil {
		t.Fatalf("tailscale proxy should be off, got %v", u)
	}
	Set(config.Proxy{URL: "http://p:3128", Tailscale: true})
	if u, _ := tailscaleHTTPProxy(target); u == nil {
		t.Fatal("tailscale HTTP proxy not used")
	}
	if u, _ := ForUpdate(req("https://github.com/x")); u != nil && u.Host == "p:3128" {
		t.Fatal("update proxy should be off")
	}
	// SOCKS5 不经 HTTP 代理钩子（由拨号器处理）
	Set(config.Proxy{URL: "socks5://p:1080", Tailscale: true})
	if u, _ := tailscaleHTTPProxy(target); u != nil {
		t.Fatalf("SOCKS5 must not be returned as HTTP proxy, got %v", u)
	}
	Set(config.Proxy{})
}

func TestScrub(t *testing.T) {
	Set(config.Proxy{URL: "http://eve:p@ss@h:8888", Tailscale: true})
	defer Set(config.Proxy{})
	in := `tshttpproxy: CONNECT response from http://eve:p%40ss@h:8888 for "x" (auth "Basic ZXZlOnBAc3M="): 200`
	got := Scrub(in)
	if got != `tshttpproxy: CONNECT response from http://eve:******@h:8888 for "x" (auth "Basic ******"): 200` {
		t.Fatalf("Scrub = %q", got)
	}
}
