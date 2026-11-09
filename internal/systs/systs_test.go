package systs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFirewallCommands(t *testing.T) {
	join := func(cmds [][]string) string {
		var b strings.Builder
		for _, c := range cmds {
			b.WriteString(strings.Join(c, " ") + "\n")
		}
		return b.String()
	}
	s := join(FirewallCommands(true))
	for _, want := range []string{
		"uci set firewall.rsagent_ts=zone\n",
		"uci add_list firewall.rsagent_ts.device=tailscale0\n",
		"uci set firewall.rsagent_ts.input=ACCEPT\n",
		"uci set firewall.rsagent_ts_lan.dest=lan\n",
		"uci commit firewall\n/etc/init.d/firewall reload\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(join(FirewallCommands(false)), "forwarding") {
		t.Error("forwarding added without lan zone")
	}
}

func TestInstallPackageUntrustedFeed(t *testing.T) {
	old := Runner
	defer func() { Runner = old }()
	var calls []string
	Runner = func(_ context.Context, out io.Writer, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) == 2 && args[0] == "add" { // 第一次：厂商源签名不受信任
			io.WriteString(out, "ERROR: tailscale: UNTRUSTED signature\n")
			return errors.New("exit status 1")
		}
		return nil
	}
	if err := installPackage(context.Background(), io.Discard, "apk", "tailscale"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != "apk add --allow-untrusted tailscale" {
		t.Fatalf("calls: %v", calls)
	}
	// 其他错误不改用 --allow-untrusted
	calls = nil
	Runner = func(_ context.Context, out io.Writer, name string, args ...string) error {
		calls = append(calls, name)
		io.WriteString(out, "ERROR: unable to select packages\n")
		return errors.New("exit status 1")
	}
	if err := installPackage(context.Background(), io.Discard, "apk", "tailscale"); err == nil || len(calls) != 1 {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
}
