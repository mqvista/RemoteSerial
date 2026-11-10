package systs

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleInit = `#!/bin/sh /etc/rc.common
start_service() {
  procd_open_instance
  procd_set_param command /usr/sbin/tailscaled
  procd_set_param env TS_DEBUG_FIREWALL_MODE="$fw_mode"
  procd_set_param respawn
  procd_close_instance
}
`

func TestSyncProxy(t *testing.T) {
	dir := t.TempDir()
	oldInit, oldEnv := initScript, proxyEnvFile
	initScript = filepath.Join(dir, "tailscale")
	proxyEnvFile = filepath.Join(dir, "rsagent", "tailscaled-proxy.sh")
	defer func() { initScript, proxyEnvFile = oldInit, oldEnv }()
	if err := os.WriteFile(initScript, []byte(sampleInit), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, _ := url.Parse("http://bob:it's@10.0.0.1:3128")

	changed, err := SyncProxy(ctx, io.Discard, u)
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v", changed, err)
	}
	script, _ := os.ReadFile(initScript)
	lines := strings.Split(string(script), "\n")
	hook := -1
	for i, l := range lines {
		if strings.Contains(l, proxyHookMark) {
			hook = i
		}
	}
	if hook < 0 || strings.TrimSpace(lines[hook+1]) != "procd_close_instance" {
		t.Fatalf("hook not before procd_close_instance:\n%s", script)
	}
	env, _ := os.ReadFile(proxyEnvFile)
	if !strings.Contains(string(env), `'HTTPS_PROXY=http://bob:it%27s@10.0.0.1:3128'`) {
		t.Fatalf("env file:\n%s", env)
	}
	if !ProxyHooked() {
		t.Fatal("ProxyHooked = false")
	}
	if q := shellQuote("a'b"); q != `'a'\''b'` {
		t.Fatalf("shellQuote = %s", q)
	}

	if changed, err := SyncProxy(ctx, io.Discard, u); err != nil || changed {
		t.Fatalf("second sync should be a no-op: changed=%v err=%v", changed, err)
	}

	// 套件升级覆盖了服务脚本：补回
	_ = os.WriteFile(initScript, []byte(sampleInit), 0o755)
	if changed, err := SyncProxy(ctx, io.Discard, u); err != nil || !changed || !ProxyHooked() {
		t.Fatalf("re-hook: changed=%v err=%v", changed, err)
	}

	s5, _ := url.Parse("socks5://10.0.0.1:1080")
	if _, err := SyncProxy(ctx, io.Discard, s5); err != nil {
		t.Fatal(err)
	}
	env, _ = os.ReadFile(proxyEnvFile)
	if !strings.Contains(string(env), "ALL_PROXY=socks5://10.0.0.1:1080") || strings.Contains(string(env), "HTTPS_PROXY") {
		t.Fatalf("socks env file:\n%s", env)
	}

	if changed, err := SyncProxy(ctx, io.Discard, nil); err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}
	script, _ = os.ReadFile(initScript)
	if string(script) != sampleInit {
		t.Fatalf("script not restored:\n%s", script)
	}
	if _, err := os.Stat(proxyEnvFile); !os.IsNotExist(err) {
		t.Fatal("env file not removed")
	}
}
