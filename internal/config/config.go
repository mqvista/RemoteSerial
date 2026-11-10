// Package config 定义 rsagent 的配置文件结构与读写。
//
// 配置文件为 JSON，位于数据目录下的 config.json。缺失时生成默认配置。
// 写入采用临时文件加重命名，避免写到一半断电留下半截文件。
package config

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"rsagent/internal/i18n"
	"strconv"
	"strings"

	"rsagent/internal/platform"
)

// CurrentVersion 是当前配置文件格式版本，用于日后迁移。
const CurrentVersion = 1

// Config 是配置文件的根结构。
type Config struct {
	Version   int       `json:"version"`
	Listen    string    `json:"listen"`             // 本机引导/控制监听地址，webview 窗口访问此地址
	LogLevel  string    `json:"logLevel"`           // debug | info | warn | error
	Language  string    `json:"language,omitempty"` // en | zh-TW；空为跟随系统
	Tailscale Tailscale `json:"tailscale"`
	Access    Access    `json:"access"`
	Update    Update    `json:"update"`
	Bridges   []Bridge  `json:"bridges"`
	Proxy     Proxy     `json:"proxy"`
	// StatusFile 非空时每 2 秒把精简状态写到这个文件（JSON），供外部显示（如 Photonicat 小屏幕）读取
	StatusFile string `json:"statusFile,omitempty"`
}

// DefaultUpdateURL 是发布清单的默认地址，指向 GitHub Releases 最新版本。
const DefaultUpdateURL = "https://github.com/mqvista/RemoteSerial/releases/latest/download/manifest.json"

// Update 是自更新设置。
type Update struct {
	URL       string `json:"url"`       // 发布清单地址，私有仓库或内网可改为自建地址
	AutoCheck bool   `json:"autoCheck"` // 启动后和每天自动检查一次，只提示不自动安装
}

// Proxy 是前置代理：部分网络要经代理才能检查更新、连上 Tailscale。
type Proxy struct {
	URL       string `json:"url,omitempty"`       // http://[用户:密码@]主机:端口，或 socks5://…
	Update    bool   `json:"update,omitempty"`    // 检查与下载更新、安装套件时使用
	Tailscale bool   `json:"tailscale,omitempty"` // Tailscale 连接控制服务器与中继服务器时使用
}

// ParseProxyURL 解析代理地址，只接受 http、https、socks5、socks5h。
func ParseProxyURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil, i18n.Errorf("代理地址格式错误: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, i18n.Errorf("代理地址必须以 http://、https:// 或 socks5:// 开头: %q", s)
	}
	if u.Hostname() == "" {
		return nil, i18n.Errorf("代理地址缺少主机: %q", s)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, i18n.Errorf("代理端口非法: %q", p)
		}
	}
	return u, nil
}

// 网络后端。
const (
	BackendEmbedded = "embedded" // 内嵌 tsnet 节点
	BackendSystem   = "system"   // 使用系统自带的 tailscaled（OpenWrt 默认）
)

// DefaultSystemSocket 是系统 tailscaled 的 LocalAPI socket。
const DefaultSystemSocket = "/var/run/tailscale/tailscaled.sock"

// Tailscale 是 Tailscale 节点的参数。auth key 不落盘，不在此结构中。
type Tailscale struct {
	Backend         string   `json:"backend,omitempty"`      // embedded | system；空为平台默认（OpenWrt 为 system）
	SystemSocket    string   `json:"systemSocket,omitempty"` // system 后端的 LocalAPI socket，空为默认路径
	Hostname        string   `json:"hostname"`               // tailnet 中的节点名
	ControlURL      string   `json:"controlURL"`             // 空为官方控制服务器；Headscale 填其地址
	UDPPort         int      `json:"udpPort"`                // WireGuard 监听端口，固定便于加防火墙规则
	WebPort         int      `json:"webPort"`                // tailnet 侧管理界面端口
	AdvertiseRoutes []string `json:"advertiseRoutes"`        // 子网路由 CIDR 列表
	Tags            []string `json:"tags"`                   // 入网申请的 tag；每次启动都要带上，否则控制服务器会要求重新认证
}

// EffectiveBackend 返回实际使用的网络后端。
func (t Tailscale) EffectiveBackend() string {
	if t.Backend != "" {
		return t.Backend
	}
	if platform.OpenWrt() {
		return BackendSystem
	}
	return BackendEmbedded
}

// Socket 返回 system 后端使用的 socket 路径。
func (t Tailscale) Socket() string {
	if t.SystemSocket != "" {
		return t.SystemSocket
	}
	return DefaultSystemSocket
}

// DefaultWebPort 是 tailnet 侧管理界面的默认端口。OpenWrt 上 80 端口被 LuCI（uhttpd）占用，
// 且系统 tailscale 与本机共用地址，改用与本机管理界面相同的 7890。
func DefaultWebPort() int {
	if platform.OpenWrt() {
		return 7890
	}
	return 80
}

// Access 是管理界面的访问控制。
type Access struct {
	AllowedLogins []string `json:"allowedLogins"` // 允许访问管理界面的 tailnet 登录名，空表示全部放行但界面告警
	// LAN 为 true 时本机管理界面监听所有地址，局域网可以访问（需登录：LuCI 一次性令牌或密码）。
	LAN bool `json:"lan,omitempty"`
	// PasswordHash 是局域网登录密码的 bcrypt 哈希，空表示未设置。GET /api/settings 不返回它。
	PasswordHash string `json:"passwordHash,omitempty"`
}

// ConsolePort 返回本机管理界面的端口。
func (c *Config) ConsolePort() int {
	_, p, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// ConsoleBind 返回本机管理界面实际要绑定的地址：允许局域网访问时绑定所有地址。
func (c *Config) ConsoleBind() string {
	if c.Access.LAN {
		return net.JoinHostPort("", strconv.Itoa(c.ConsolePort()))
	}
	return c.Listen
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

// normalize 把为 nil 的列表换成空列表：配置文件里写成 null 时，界面拿到的也是 []。
func (c *Config) normalize() {
	if c.Access.AllowedLogins == nil {
		c.Access.AllowedLogins = []string{}
	}
	if c.Tailscale.AdvertiseRoutes == nil {
		c.Tailscale.AdvertiseRoutes = []string{}
	}
	if c.Tailscale.Tags == nil {
		c.Tailscale.Tags = []string{}
	}
	if c.Bridges == nil {
		c.Bridges = []Bridge{}
	}
}

// PeekLanguage 只读取配置里的语言设置，文件不存在或无法解析时返回空串，不会创建文件。
func PeekLanguage(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var v struct {
		Language string `json:"language"`
	}
	_ = json.Unmarshal(data, &v)
	return v.Language
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
			WebPort:         DefaultWebPort(),
			AdvertiseRoutes: []string{},
			Tags:            []string{},
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
			return nil, false, i18n.Errorf("写入默认配置: %w", err)
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg = Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, false, i18n.Errorf("解析 %s: %w", path, err)
	}
	if cfg.Version > CurrentVersion {
		return nil, false, i18n.Errorf("配置版本 %d 高于程序支持的 %d", cfg.Version, CurrentVersion)
	}
	cfg.Version = CurrentVersion
	cfg.normalize()
	if cfg.Update.URL == "" {
		cfg.Update.URL = DefaultUpdateURL
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, i18n.Errorf("配置校验失败: %w", err)
	}
	return cfg, false, nil
}

// Save 原子写入配置文件。
func (c *Config) Save(path string) error {
	c.normalize()
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
		return i18n.New("listen 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return i18n.Errorf("listen 格式错误: %w", err)
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return i18n.Errorf("logLevel 非法: %q", c.LogLevel)
	}
	if c.Language != "" && c.Language != "en" && c.Language != "zh-TW" {
		return i18n.Errorf("language 非法: %q", c.Language)
	}
	if c.Tailscale.Hostname == "" {
		return i18n.New("tailscale.hostname 不能为空")
	}
	switch c.Tailscale.Backend {
	case "", BackendEmbedded, BackendSystem:
	default:
		return i18n.Errorf("tailscale.backend 非法: %q", c.Tailscale.Backend)
	}
	if c.Tailscale.UDPPort < 0 || c.Tailscale.UDPPort > 65535 {
		return i18n.Errorf("tailscale.udpPort 越界: %d", c.Tailscale.UDPPort)
	}
	if c.Tailscale.WebPort < 1 || c.Tailscale.WebPort > 65535 {
		return i18n.Errorf("tailscale.webPort 越界: %d", c.Tailscale.WebPort)
	}
	for _, r := range c.Tailscale.AdvertiseRoutes {
		if _, _, err := net.ParseCIDR(r); err != nil {
			return i18n.Errorf("advertiseRoutes 含非法 CIDR %q", r)
		}
	}
	for _, t := range c.Tailscale.Tags {
		if !strings.HasPrefix(t, "tag:") || len(t) <= 4 {
			return i18n.Errorf("tailscale.tags 含非法 tag %q，必须形如 tag:name", t)
		}
	}
	if c.StatusFile != "" && !filepath.IsAbs(c.StatusFile) {
		return i18n.Errorf("statusFile 必须是绝对路径: %q", c.StatusFile)
	}
	if c.Proxy.URL != "" {
		if _, err := ParseProxyURL(c.Proxy.URL); err != nil {
			return err
		}
	} else if c.Proxy.Update || c.Proxy.Tailscale {
		return i18n.New("启用代理时必须填写代理地址")
	}
	if u := c.Update.URL; u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return i18n.Errorf("update.url 必须是 http(s) 地址: %q", u)
	}

	ids := map[string]bool{}
	ports := map[int]string{}
	localPorts := map[int]string{}
	for i, b := range c.Bridges {
		if b.ID == "" {
			return i18n.Errorf("bridges[%d].id 不能为空", i)
		}
		if ids[b.ID] {
			return i18n.Errorf("bridge id 重复: %q", b.ID)
		}
		ids[b.ID] = true
		if b.Port == "" && b.Match == nil {
			return i18n.Errorf("bridge %q 需要 port 或 match", b.ID)
		}
		if b.Baud <= 0 {
			return i18n.Errorf("bridge %q baud 非法: %d", b.ID, b.Baud)
		}
		if b.DataBits < 5 || b.DataBits > 8 {
			return i18n.Errorf("bridge %q dataBits 非法: %d", b.ID, b.DataBits)
		}
		switch strings.ToUpper(b.Parity) {
		case "N", "E", "O", "M", "S":
		default:
			return i18n.Errorf("bridge %q parity 非法: %q", b.ID, b.Parity)
		}
		switch b.StopBits {
		case 1, 1.5, 2:
		default:
			return i18n.Errorf("bridge %q stopBits 非法: %v", b.ID, b.StopBits)
		}
		switch b.Mode {
		case "raw", "rfc2217":
		default:
			return i18n.Errorf("bridge %q mode 非法: %q", b.ID, b.Mode)
		}
		switch b.ClientPolicy {
		case "reject", "replace":
		default:
			return i18n.Errorf("bridge %q clientPolicy 非法: %q", b.ID, b.ClientPolicy)
		}
		if b.TCPPort < 1 || b.TCPPort > 65535 {
			return i18n.Errorf("bridge %q tcpPort 越界: %d", b.ID, b.TCPPort)
		}
		if other, dup := ports[b.TCPPort]; dup {
			return i18n.Errorf("bridge %q 与 %q 的 tcpPort 重复: %d", b.ID, other, b.TCPPort)
		}
		ports[b.TCPPort] = b.ID
		if b.LocalPort < 0 || b.LocalPort > 65535 {
			return i18n.Errorf("bridge %q localPort 越界: %d", b.ID, b.LocalPort)
		}
		if b.LocalPort > 0 {
			if other, dup := localPorts[b.LocalPort]; dup {
				return i18n.Errorf("bridge %q 与 %q 的 localPort 重复: %d", b.ID, other, b.LocalPort)
			}
			localPorts[b.LocalPort] = b.ID
		}
		if b.IdleTimeoutSec < 0 {
			return i18n.Errorf("bridge %q idleTimeoutSec 不能为负", b.ID)
		}
	}
	return nil
}
