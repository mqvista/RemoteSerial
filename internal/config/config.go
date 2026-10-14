// Package config 定义 rsagent 的配置文件结构与读写。
//
// 配置文件为 JSON，位于数据目录下的 config.json。缺失时生成默认配置。
// 写入采用临时文件加重命名，避免写到一半断电留下半截文件。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// CurrentVersion 是当前配置文件格式版本，用于日后迁移。
const CurrentVersion = 1

// Config 是配置文件的根结构。
type Config struct {
	Version   int       `json:"version"`
	Listen    string    `json:"listen"`   // 本机引导/控制监听地址，webview 窗口访问此地址
	LogLevel  string    `json:"logLevel"` // debug | info | warn | error
	Tailscale Tailscale `json:"tailscale"`
	Access    Access    `json:"access"`
	Update    Update    `json:"update"`
	Bridges   []Bridge  `json:"bridges"`
}

// DefaultUpdateURL 是发布清单的默认地址，指向 GitHub Releases 最新版本。
const DefaultUpdateURL = "https://github.com/mqvista/RemoteSerial/releases/latest/download/manifest.json"

// Update 是自更新设置。
type Update struct {
	URL       string `json:"url"`       // 发布清单地址，私有仓库或内网可改为自建地址
	AutoCheck bool   `json:"autoCheck"` // 启动后和每天自动检查一次，只提示不自动安装
}

// Tailscale 是内嵌 tsnet 节点的参数。auth key 不落盘，不在此结构中。
type Tailscale struct {
	Hostname        string   `json:"hostname"`        // tailnet 中的节点名
	ControlURL      string   `json:"controlURL"`      // 空为官方控制服务器；Headscale 填其地址
	UDPPort         int      `json:"udpPort"`         // WireGuard 监听端口，固定便于加防火墙规则
	WebPort         int      `json:"webPort"`         // tailnet 侧管理界面端口
	AdvertiseRoutes []string `json:"advertiseRoutes"` // 子网路由 CIDR 列表
}

// Access 是 tailnet 侧访问控制。
type Access struct {
	AllowedLogins []string `json:"allowedLogins"` // 允许访问管理界面的 tailnet 登录名，空表示全部放行但界面告警
}

// Bridge 是一个串口到 TCP 的桥接定义。
type Bridge struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Port           string     `json:"port"`            // 串口名，如 COM3 或 /dev/ttyUSB0
	Match          *PortMatch `json:"match,omitempty"` // 可选，按 USB 标识匹配，COM 号漂移时兜底
	Baud           int        `json:"baud"`
	DataBits       int        `json:"dataBits"`
	Parity         string     `json:"parity"`   // N | E | O | M | S
	StopBits       float64    `json:"stopBits"` // 1 | 1.5 | 2
	Mode           string     `json:"mode"`     // raw | rfc2217
	TCPPort        int        `json:"tcpPort"`
	LocalPort      int        `json:"localPort,omitempty"` // 可选：同时在 127.0.0.1 监听，供远端本机工具使用
	ClientPolicy   string     `json:"clientPolicy"`        // reject | replace
	IdleTimeoutSec int        `json:"idleTimeoutSec"`
	Enabled        bool       `json:"enabled"`
}

// PortMatch 按 USB VID/PID/序列号匹配串口。
type PortMatch struct {
	VID    string `json:"vid"`
	PID    string `json:"pid"`
	Serial string `json:"serial"`
}

// Default 返回默认配置。
func Default() *Config {
	host, _ := os.Hostname()
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		host = "agent"
	}
	return &Config{
		Version:  CurrentVersion,
		Listen:   "127.0.0.1:7890",
		LogLevel: "info",
		Tailscale: Tailscale{
			Hostname:        "rs-" + host,
			ControlURL:      "",
			UDPPort:         41641,
			WebPort:         80,
			AdvertiseRoutes: []string{},
		},
		Access:  Access{AllowedLogins: []string{}},
		Update:  Update{URL: DefaultUpdateURL, AutoCheck: true},
		Bridges: []Bridge{},
	}
}

// Load 读取配置文件。文件不存在时写入默认配置并返回 created=true。
func Load(path string) (cfg *Config, created bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg = Default()
		if err := cfg.Save(path); err != nil {
			return nil, false, fmt.Errorf("写入默认配置: %w", err)
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg = Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, false, fmt.Errorf("解析 %s: %w", path, err)
	}
	if cfg.Version > CurrentVersion {
		return nil, false, fmt.Errorf("配置版本 %d 高于程序支持的 %d", cfg.Version, CurrentVersion)
	}
	cfg.Version = CurrentVersion
	if cfg.Update.URL == "" {
		cfg.Update.URL = DefaultUpdateURL
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, fmt.Errorf("配置校验失败: %w", err)
	}
	return cfg, false, nil
}

// Save 原子写入配置文件。
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Validate 检查配置自洽性。
func (c *Config) Validate() error {
	if c.Listen == "" {
		return errors.New("listen 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen 格式错误: %w", err)
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logLevel 非法: %q", c.LogLevel)
	}
	if c.Tailscale.Hostname == "" {
		return errors.New("tailscale.hostname 不能为空")
	}
	if c.Tailscale.UDPPort < 0 || c.Tailscale.UDPPort > 65535 {
		return fmt.Errorf("tailscale.udpPort 越界: %d", c.Tailscale.UDPPort)
	}
	if c.Tailscale.WebPort < 1 || c.Tailscale.WebPort > 65535 {
		return fmt.Errorf("tailscale.webPort 越界: %d", c.Tailscale.WebPort)
	}
	for _, r := range c.Tailscale.AdvertiseRoutes {
		if _, _, err := net.ParseCIDR(r); err != nil {
			return fmt.Errorf("advertiseRoutes 含非法 CIDR %q", r)
		}
	}
	if u := c.Update.URL; u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return fmt.Errorf("update.url 必须是 http(s) 地址: %q", u)
	}

	ids := map[string]bool{}
	ports := map[int]string{}
	localPorts := map[int]string{}
	for i, b := range c.Bridges {
		if b.ID == "" {
			return fmt.Errorf("bridges[%d].id 不能为空", i)
		}
		if ids[b.ID] {
			return fmt.Errorf("bridge id 重复: %q", b.ID)
		}
		ids[b.ID] = true
		if b.Port == "" && b.Match == nil {
			return fmt.Errorf("bridge %q 需要 port 或 match", b.ID)
		}
		if b.Baud <= 0 {
			return fmt.Errorf("bridge %q baud 非法: %d", b.ID, b.Baud)
		}
		if b.DataBits < 5 || b.DataBits > 8 {
			return fmt.Errorf("bridge %q dataBits 非法: %d", b.ID, b.DataBits)
		}
		switch strings.ToUpper(b.Parity) {
		case "N", "E", "O", "M", "S":
		default:
			return fmt.Errorf("bridge %q parity 非法: %q", b.ID, b.Parity)
		}
		switch b.StopBits {
		case 1, 1.5, 2:
		default:
			return fmt.Errorf("bridge %q stopBits 非法: %v", b.ID, b.StopBits)
		}
		switch b.Mode {
		case "raw", "rfc2217":
		default:
			return fmt.Errorf("bridge %q mode 非法: %q", b.ID, b.Mode)
		}
		switch b.ClientPolicy {
		case "reject", "replace":
		default:
			return fmt.Errorf("bridge %q clientPolicy 非法: %q", b.ID, b.ClientPolicy)
		}
		if b.TCPPort < 1 || b.TCPPort > 65535 {
			return fmt.Errorf("bridge %q tcpPort 越界: %d", b.ID, b.TCPPort)
		}
		if other, dup := ports[b.TCPPort]; dup {
			return fmt.Errorf("bridge %q 与 %q 的 tcpPort 重复: %d", b.ID, other, b.TCPPort)
		}
		ports[b.TCPPort] = b.ID
		if b.LocalPort < 0 || b.LocalPort > 65535 {
			return fmt.Errorf("bridge %q localPort 越界: %d", b.ID, b.LocalPort)
		}
		if b.LocalPort > 0 {
			if other, dup := localPorts[b.LocalPort]; dup {
				return fmt.Errorf("bridge %q 与 %q 的 localPort 重复: %d", b.ID, other, b.LocalPort)
			}
			localPorts[b.LocalPort] = b.ID
		}
		if b.IdleTimeoutSec < 0 {
			return fmt.Errorf("bridge %q idleTimeoutSec 不能为负", b.ID)
		}
	}
	return nil
}
