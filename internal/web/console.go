package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"rsagent/internal/i18n"
	"strconv"
	"time"

	"rsagent/internal/platform"
	"rsagent/internal/systs"
)

// ConsoleInfo 是本机管理界面的端口与访问设置。
type ConsoleInfo struct {
	Port        int    `json:"port"`
	LAN         bool   `json:"lan"`
	Listen      string `json:"listen"`
	Bind        string `json:"bind"`
	WebPort     int    `json:"webPort"` // tailnet 侧端口
	PasswordSet bool   `json:"passwordSet"`
	Live        bool   `json:"live"` // 修改立即生效；false 表示需要重启
	Warning     string `json:"warning,omitempty"`
}

func (s *Server) consoleInfo() ConsoleInfo {
	c := s.deps.Config
	return ConsoleInfo{
		Port:        c.ConsolePort(),
		LAN:         c.Access.LAN,
		Listen:      c.Listen,
		Bind:        c.ConsoleBind(),
		WebPort:     c.Tailscale.WebPort,
		PasswordSet: c.Access.PasswordHash != "",
		Live:        s.deps.Console != nil,
	}
}

// GET /api/console
func (s *Server) handleConsoleGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.consoleInfo())
}

// reservedPorts 是 OpenWrt 上常被系统服务占用的端口（ssh、dns、LuCI）。
var reservedPorts = map[int]string{22: "SSH", 53: "DNS", 80: "LuCI (HTTP)", 443: "LuCI (HTTPS)"}

// checkConsolePort 检查新端口：范围、系统服务、桥接端口。是否已被占用由实际监听判断。
func (s *Server) checkConsolePort(port int) error {
	if port < 1 || port > 65535 {
		return i18n.Errorf("端口必须在 1 到 65535 之间: %d", port)
	}
	if name, ok := reservedPorts[port]; ok && platform.OpenWrt() {
		return i18n.Errorf("端口 %d 通常由 %s 使用，请换一个", port, name)
	}
	for _, b := range s.deps.Config.Bridges {
		if b.TCPPort == port || b.LocalPort == port {
			name := b.Name
			if name == "" {
				name = b.ID
			}
			return i18n.Errorf("端口 %d 已被桥接 %q 使用", port, name)
		}
	}
	return nil
}

// PUT /api/console {"port": 7890, "lan": true}
//
// 先在新地址开始监听，成功后才关闭旧的；失败时保持原样并返回错误，不会把自己锁在门外。
func (s *Server) handleConsolePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port *int  `json:"port"`
		LAN  *bool `json:"lan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, i18n.T("请求体格式错误"))
		return
	}
	cfg := s.deps.Config
	oldPort := cfg.ConsolePort()
	port, lan := oldPort, cfg.Access.LAN
	if req.Port != nil {
		port = *req.Port
	}
	if req.LAN != nil {
		lan = *req.LAN
	}
	if !lan && SourceOf(r) == SourceLAN {
		writeError(w, http.StatusForbidden, i18n.T("从局域网关闭局域网访问会切断你自己的连接，请从 LuCI 或 tailnet 操作"))
		return
	}
	if port != oldPort {
		if err := s.checkConsolePort(port); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if port == oldPort && lan == cfg.Access.LAN {
		writeJSON(w, http.StatusOK, s.consoleInfo())
		return
	}

	host, _, _ := net.SplitHostPort(cfg.Listen)
	backup := *cfg
	cfg.Listen = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.Access.LAN = lan
	// OpenWrt 上两侧共用一个端口：原来相同就一起改
	if cfg.Tailscale.WebPort == oldPort {
		cfg.Tailscale.WebPort = port
	}
	if err := cfg.Validate(); err != nil {
		*cfg = backup
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.deps.Console != nil {
		if err := s.deps.Console.Apply(cfg.ConsoleBind(), cfg.Tailscale.WebPort); err != nil {
			*cfg = backup
			writeError(w, http.StatusConflict, i18n.Sprintf("无法监听端口 %d: %v", port, err))
			return
		}
	}
	if err := cfg.Save(s.deps.Paths.ConfigFile); err != nil {
		s.deps.Log.Error(i18n.T("保存配置失败"), "err", err)
	}
	info := s.consoleInfo()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	if err := systs.SyncConsoleFirewall(fctx, port, lan); err != nil {
		s.deps.Log.Warn(i18n.T("同步管理界面防火墙规则失败"), "err", err)
		info.Warning = err.Error()
	}
	cancel()
	s.deps.Log.Info(i18n.T("已修改管理界面端口与局域网访问"), "port", port, "lan", lan, "live", info.Live, "by", requesterLabel(r))
	s.hub.Notify()
	writeJSON(w, http.StatusOK, info)
}
