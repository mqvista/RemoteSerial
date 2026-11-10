package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"rsagent/internal/config"
	"rsagent/internal/i18n"
	"rsagent/internal/netproxy"
	"rsagent/internal/platform"
)

// ProxyControl 由 app 实现：套用前置代理设置。
type ProxyControl interface {
	// Apply 套用新设置（配置已保存）。需要重启或重新连线 Tailscale 时在背景进行。
	Apply(old, p config.Proxy)
	// SystemState 报告是否由 rsagent 管理系统 tailscaled 的代理、服务脚本是否已带上代理设置、最近的错误。
	SystemState() (managed, hooked bool, errText string)
}

type proxyInfo struct {
	URL       string `json:"url"` // 密码以 ****** 显示
	Update    bool   `json:"update"`
	Tailscale bool   `json:"tailscale"`
	Backend   string `json:"backend"` // embedded | system
	// System 为真表示 Tailscale 代理写在系统 tailscaled 的服务脚本里（OpenWrt）
	System       bool   `json:"system"`
	SystemHooked bool   `json:"systemHooked"`
	SystemError  string `json:"systemError,omitempty"`
	// PackageHTTPOnly 为真表示更新会用 SOCKS5 代理，但套件管理器（opkg、apk）只支持 HTTP 代理
	PackageHTTPOnly bool `json:"packageHttpOnly"`
}

func (s *Server) proxyInfo() proxyInfo {
	p := s.deps.Config.Proxy
	info := proxyInfo{URL: netproxy.Redact(p.URL), Update: p.Update, Tailscale: p.Tailscale, Backend: config.BackendEmbedded}
	if s.deps.Node != nil && s.deps.Node.System() {
		info.Backend = config.BackendSystem
	}
	if s.deps.Proxy != nil {
		info.System, info.SystemHooked, info.SystemError = s.deps.Proxy.SystemState()
	}
	if p.Update && platform.OpenWrt() {
		if u, err := config.ParseProxyURL(p.URL); err == nil && !netproxy.IsHTTP(u) {
			info.PackageHTTPOnly = true
		}
	}
	return info
}

// GET /api/proxy
func (s *Server) handleProxyGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.proxyInfo())
}

// PUT /api/proxy
func (s *Server) handleProxyPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL       string `json:"url"`
		Update    bool   `json:"update"`
		Tailscale bool   `json:"tailscale"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, i18n.Sprintf("请求体格式错误: %v", err))
		return
	}
	cfg := s.deps.Config
	old := cfg.Proxy
	p := config.Proxy{URL: netproxy.Restore(strings.TrimSpace(req.URL), old.URL), Update: req.Update, Tailscale: req.Tailscale}
	cfg.Proxy = p
	if err := cfg.Validate(); err != nil {
		cfg.Proxy = old
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cfg.Save(s.deps.Paths.ConfigFile); err != nil {
		cfg.Proxy = old
		writeError(w, http.StatusInternalServerError, i18n.Sprintf("保存配置失败: %v", err))
		return
	}
	if s.deps.Proxy != nil {
		s.deps.Proxy.Apply(old, p)
	}
	s.deps.Log.Info(i18n.T("代理设置已更新"), "proxy", netproxy.Redact(p.URL), "update", p.Update, "tailscale", p.Tailscale, "by", requesterLabel(r))
	s.hub.Notify()
	writeJSON(w, http.StatusOK, s.proxyInfo())
}
