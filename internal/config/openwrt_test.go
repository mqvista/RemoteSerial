package config

import (
	"testing"

	"rsagent/internal/platform"
)

func TestOpenWrtDefaults(t *testing.T) {
	defer platform.OverrideForTest(true)()
	c := Default()
	if c.Tailscale.WebPort != 7890 {
		t.Errorf("webPort = %d, want 7890 (80 is taken by LuCI)", c.Tailscale.WebPort)
	}
	if b := c.Tailscale.EffectiveBackend(); b != BackendSystem {
		t.Errorf("backend = %q, want system", b)
	}
	c.Tailscale.Backend = BackendEmbedded
	if b := c.Tailscale.EffectiveBackend(); b != BackendEmbedded {
		t.Errorf("explicit backend ignored: %q", b)
	}
	if c.Tailscale.Socket() != DefaultSystemSocket {
		t.Errorf("socket = %q", c.Tailscale.Socket())
	}
	p := ResolvePaths("/etc/rsagent")
	if p.LogDir != "/tmp/rsagent/logs" || p.ConfigFile != "/etc/rsagent/config.json" {
		t.Errorf("paths = %+v", p)
	}
	c.Tailscale.Backend = "bogus"
	if c.Validate() == nil {
		t.Error("bogus backend accepted")
	}
}

func TestNonOpenWrtDefaults(t *testing.T) {
	defer platform.OverrideForTest(false)()
	c := Default()
	if c.Tailscale.WebPort != 80 || c.Tailscale.EffectiveBackend() != BackendEmbedded {
		t.Errorf("webPort=%d backend=%q", c.Tailscale.WebPort, c.Tailscale.EffectiveBackend())
	}
}
