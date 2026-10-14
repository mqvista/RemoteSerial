package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/tsnode"
)

// ProvisionOptions 是无人值守入网的参数，供安装程序和批量部署脚本使用。
type ProvisionOptions struct {
	DataDir       string
	Listen        string
	AuthKey       string   // 普通 auth key 或 OAuth client secret（tskey-client-…）
	Tags          []string // 入网申请的 tag；OAuth secret 时必填
	Hostname      string   // 空则沿用配置
	ControlURL    string
	ControlURLSet bool // ControlURL 是否由调用方指定（允许显式清空）
	Timeout       time.Duration
	Log           *slog.Logger
}

// ProvisionResult 是入网结果。
type ProvisionResult struct {
	Hostname string
	IP       string
	DNSName  string
	ViaCore  bool // 由运行中的 core 完成
}

// Provision 用 auth key 完成入网并把状态落盘。
//
// 本机已有 core 在运行时交给它执行（否则两个 tsnet 实例会争用状态目录）；
// 否则临时起一个节点，上线后停止，之后正常启动的 core 会自动上线。
func Provision(ctx context.Context, o ProvisionOptions) (ProvisionResult, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	o.AuthKey = strings.TrimSpace(o.AuthKey)
	if o.AuthKey == "" {
		return ProvisionResult{}, errors.New("缺少 auth key：用 --auth-key 或环境变量 RSAGENT_AUTH_KEY 提供")
	}
	o.Tags = SplitTags(strings.Join(o.Tags, ","))
	for _, t := range o.Tags {
		if !strings.HasPrefix(t, "tag:") {
			return ProvisionResult{}, fmt.Errorf("tag 必须以 tag: 开头: %q", t)
		}
	}

	paths := config.ResolvePaths(o.DataDir)
	if err := paths.Ensure(); err != nil {
		return ProvisionResult{}, fmt.Errorf("创建数据目录 %s: %w", paths.DataDir, err)
	}
	cfg, _, err := config.Load(paths.ConfigFile)
	if err != nil {
		return ProvisionResult{}, err
	}
	changed := false
	if h := strings.TrimSpace(o.Hostname); h != "" && h != cfg.Tailscale.Hostname {
		cfg.Tailscale.Hostname = h
		changed = true
	}
	if o.ControlURLSet && strings.TrimSpace(o.ControlURL) != cfg.Tailscale.ControlURL {
		cfg.Tailscale.ControlURL = strings.TrimSpace(o.ControlURL)
		changed = true
	}
	listen := cfg.Listen
	if o.Listen != "" {
		listen = o.Listen
	}

	if coreResponds(listen) {
		return provisionViaCore(ctx, listen, o, cfg.Tailscale.Hostname, cfg.Tailscale.ControlURL)
	}
	if changed {
		if err := cfg.Save(paths.ConfigFile); err != nil {
			return ProvisionResult{}, fmt.Errorf("保存配置: %w", err)
		}
	}

	node := tsnode.New(tsnode.Config{
		StateDir:        paths.StateDir,
		Hostname:        cfg.Tailscale.Hostname,
		ControlURL:      cfg.Tailscale.ControlURL,
		UDPPort:         uint16(cfg.Tailscale.UDPPort),
		AdvertiseRoutes: cfg.Tailscale.AdvertiseRoutes,
	}, o.Log.With("mod", "tsnode"), tsnode.Hooks{})
	if err := node.Start(ctx, o.AuthKey, o.Tags); err != nil {
		return ProvisionResult{}, err
	}
	defer node.Stop()

	deadline := time.Now().Add(o.Timeout)
	for {
		snap := node.Snapshot()
		switch snap.State {
		case tsnode.StateRunning:
			return ProvisionResult{Hostname: snap.Hostname, IP: snap.IP, DNSName: snap.DNSName}, nil
		case tsnode.StateNeedsLogin, tsnode.StateError:
			if snap.Message != "" {
				return ProvisionResult{}, fmt.Errorf("入网失败: %s", snap.Message)
			}
		case tsnode.StateNeedsMachineAuth:
			if time.Now().After(deadline) {
				return ProvisionResult{}, errors.New("设备等待管理员批准：auth key 没有 pre-authorized，或 tag 未在 ACL 的 tagOwners 中声明")
			}
		}
		if time.Now().After(deadline) {
			return ProvisionResult{}, fmt.Errorf("上线超时（%s），当前状态 %s %s", o.Timeout, snap.State, snap.Message)
		}
		select {
		case <-ctx.Done():
			return ProvisionResult{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func provisionViaCore(ctx context.Context, listen string, o ProvisionOptions, hostname, controlURL string) (ProvisionResult, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	body, _ := json.Marshal(map[string]any{
		"authKey":    o.AuthKey,
		"hostname":   strings.TrimSpace(o.Hostname),
		"controlURL": controlURL,
		"tags":       o.Tags,
	})
	if o.ControlURLSet {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		m["controlURL"] = strings.TrimSpace(o.ControlURL)
		body, _ = json.Marshal(m)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listen+"/api/tailscale/login", bytes.NewReader(body))
	if err != nil {
		return ProvisionResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("调用运行中的 core: %w", err)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return ProvisionResult{}, fmt.Errorf("core 拒绝登录: %s", e.Error)
	}

	deadline := time.Now().Add(o.Timeout)
	for {
		var snap tsnode.Snapshot
		if r, err := client.Get("http://" + listen + "/api/tailscale/status"); err == nil {
			_ = json.NewDecoder(r.Body).Decode(&snap)
			r.Body.Close()
		}
		switch snap.State {
		case tsnode.StateRunning:
			return ProvisionResult{Hostname: snap.Hostname, IP: snap.IP, DNSName: snap.DNSName, ViaCore: true}, nil
		case tsnode.StateNeedsLogin, tsnode.StateError:
			if snap.Message != "" {
				return ProvisionResult{}, fmt.Errorf("入网失败: %s", snap.Message)
			}
		}
		if time.Now().After(deadline) {
			return ProvisionResult{}, fmt.Errorf("上线超时（%s），当前状态 %s %s", o.Timeout, snap.State, snap.Message)
		}
		select {
		case <-ctx.Done():
			return ProvisionResult{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// SplitTags 把 "tag:a, tag:b" 拆成列表，去空去重。
func SplitTags(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
