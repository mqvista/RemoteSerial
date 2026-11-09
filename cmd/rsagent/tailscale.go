package main

import (
	"encoding/json"
	"fmt"
	"os"

	"rsagent/internal/config"
	"rsagent/internal/systs"
)

// cmdTailscale 管理系统自带的 tailscale（OpenWrt），以及选择网络后端。
func cmdTailscale(args []string) error {
	const usageLine = "usage: rsagent tailscale status|install|start|firewall|backend <embedded|system> [options]"
	if len(args) == 0 {
		return fmt.Errorf(usageLine)
	}
	op, rest := args[0], args[1:]
	var value string
	if op == "backend" {
		if len(rest) == 0 || rest[0] == "" || rest[0][0] == '-' {
			return fmt.Errorf(usageLine)
		}
		value, rest = rest[0], rest[1:]
	}
	o, err := parseCommon("tailscale "+op, rest, false)
	if err != nil {
		return err
	}
	paths := config.ResolvePaths(o.DataDir)
	ctx, stop := signalContext()
	defer stop()

	switch op {
	case "status":
		cfg, _, err := config.Load(paths.ConfigFile)
		if err != nil {
			return err
		}
		out := struct {
			Backend string     `json:"backend"`
			System  systs.Info `json:"system"`
		}{cfg.Tailscale.EffectiveBackend(), systs.Detect(cfg.Tailscale.Socket())}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case "install":
		return systs.Install(ctx, os.Stdout)
	case "start":
		if err := systs.StartService(ctx, os.Stdout); err != nil {
			return err
		}
		return systs.EnsureFirewall(ctx, os.Stdout)
	case "firewall":
		return systs.EnsureFirewall(ctx, os.Stdout)
	case "backend":
		if value != config.BackendEmbedded && value != config.BackendSystem {
			return fmt.Errorf("backend must be %q or %q", config.BackendEmbedded, config.BackendSystem)
		}
		if err := paths.Ensure(); err != nil {
			return err
		}
		cfg, _, err := config.Load(paths.ConfigFile)
		if err != nil {
			return err
		}
		cfg.Tailscale.Backend = value
		if err := cfg.Save(paths.ConfigFile); err != nil {
			return err
		}
		fmt.Printf("tailscale backend set to %s; restart rsagent to apply\n", value)
		return nil
	}
	return fmt.Errorf("unknown tailscale operation %q", op)
}
