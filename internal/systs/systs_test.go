package systs

import (
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
