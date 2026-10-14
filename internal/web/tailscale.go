package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

type loginRequest struct {
	AuthKey    string `json:"authKey"`
	Hostname   string `json:"hostname"`
	ControlURL string `json:"controlURL"`
}

// POST /api/tailscale/login：首次入网。auth key 只在内存中传递，不记录日志。
func (s *Server) handleTailscaleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	req.AuthKey = strings.TrimSpace(req.AuthKey)
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.ControlURL = strings.TrimSpace(req.ControlURL)
	if req.AuthKey == "" && !s.deps.Node.HasState() {
		writeError(w, http.StatusBadRequest, "auth key 不能为空")
		return
	}

	if req.Hostname != "" || req.ControlURL != s.deps.Config.Tailscale.ControlURL {
		if err := s.deps.Node.UpdateConfig(req.Hostname, req.ControlURL); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if req.Hostname != "" {
			s.deps.Config.Tailscale.Hostname = req.Hostname
		}
		s.deps.Config.Tailscale.ControlURL = req.ControlURL
		if err := s.deps.Config.Save(s.deps.Paths.ConfigFile); err != nil {
			s.deps.Log.Error("保存配置失败", "err", err)
		}
	}

	if err := s.deps.Node.Start(r.Context(), req.AuthKey); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.deps.Log.Info("已发起 Tailscale 登录", "hostname", s.deps.Config.Tailscale.Hostname, "by", requesterLabel(r))
	writeJSON(w, http.StatusAccepted, s.deps.Node.Snapshot())
}

// POST /api/tailscale/start：用已有状态启动。
func (s *Server) handleTailscaleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Node.Start(r.Context(), ""); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.deps.Node.Snapshot())
}

// POST /api/tailscale/stop：停止但保留登录状态。
func (s *Server) handleTailscaleStop(w http.ResponseWriter, r *http.Request) {
	if SourceOf(r) == SourceTailnet {
		writeError(w, http.StatusForbidden, "从 tailnet 侧停止节点会切断你自己的连接，请在远端本机操作")
		return
	}
	if err := s.deps.Node.Stop(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Node.Snapshot())
}

// POST /api/tailscale/logout：退出并清除状态。
func (s *Server) handleTailscaleLogout(w http.ResponseWriter, r *http.Request) {
	if SourceOf(r) == SourceTailnet {
		writeError(w, http.StatusForbidden, "从 tailnet 侧退出登录会切断你自己的连接，请在远端本机操作")
		return
	}
	if err := s.deps.Node.Logout(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Node.Snapshot())
}

// GET /api/tailscale/status
func (s *Server) handleTailscaleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Node.Snapshot())
}

// GET /api/tailscale/peers
func (s *Server) handleTailscalePeers(w http.ResponseWriter, r *http.Request) {
	peers, err := s.deps.Node.Peers(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
}

// GET /api/tailscale/routes
func (s *Server) handleTailscaleRoutesGet(w http.ResponseWriter, r *http.Request) {
	rs, err := s.deps.Node.Routes(r.Context())
	if err != nil {
		s.deps.Log.Warn("读取路由状态失败", "err", err)
	}
	writeJSON(w, http.StatusOK, rs)
}

// PUT /api/tailscale/routes {"routes": ["192.168.10.0/24"]}
func (s *Server) handleTailscaleRoutesPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Routes []string `json:"routes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	clean := make([]string, 0, len(req.Routes))
	for _, rt := range req.Routes {
		if rt = strings.TrimSpace(rt); rt != "" {
			clean = append(clean, rt)
		}
	}
	s.deps.Config.Tailscale.AdvertiseRoutes = clean
	if err := s.deps.Config.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.deps.Node.SetAdvertiseRoutesConfig(r.Context(), clean); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.deps.Config.Save(s.deps.Paths.ConfigFile); err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	s.deps.Log.Info("已更新子网路由", "routes", clean, "by", requesterLabel(r))
	rs, _ := s.deps.Node.Routes(r.Context())
	writeJSON(w, http.StatusOK, rs)
}

func requesterLabel(r *http.Request) string {
	if id := IdentityOf(r); id != nil {
		return id.LoginName + "@" + id.Node
	}
	return "local"
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
