package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 配置里写成 null 的列表读出来要是空列表，保存时也不能再写回 null（前端会对它做展开）。
func TestNullListsNormalized(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"version":1,"listen":"127.0.0.1:7890","logLevel":"info",
		"tailscale":{"hostname":"rs-x","udpPort":41641,"webPort":80,"advertiseRoutes":null,"tags":null},
		"access":{"allowedLogins":null},"bridges":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Access.AllowedLogins == nil || cfg.Tailscale.Tags == nil || cfg.Tailscale.AdvertiseRoutes == nil || cfg.Bridges == nil {
		t.Fatalf("null 列表没有换成空列表: %+v", cfg)
	}
	cfg.Access.AllowedLogins = nil
	if err := cfg.Save(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "null") {
		t.Fatalf("保存后仍有 null:\n%s", data)
	}
}
