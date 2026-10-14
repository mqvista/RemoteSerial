package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/buildinfo"
	"rsagent/internal/logging"
	"rsagent/internal/tsnode"
)

func (s *Server) routes() {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/status", s.handleStatus)
	api.HandleFunc("GET /api/version", s.handleVersion)
	api.HandleFunc("GET /api/logs", s.handleLogs)
	api.HandleFunc("GET /api/settings", s.handleSettingsGet)
	api.HandleFunc("PUT /api/settings", s.handleSettingsPut)
	api.HandleFunc("GET /api/network/interfaces", s.handleInterfaces)

	api.HandleFunc("GET /api/tailscale/status", s.handleTailscaleStatus)
	api.HandleFunc("POST /api/tailscale/login", s.handleTailscaleLogin)
	api.HandleFunc("POST /api/tailscale/start", s.handleTailscaleStart)
	api.HandleFunc("POST /api/tailscale/stop", s.handleTailscaleStop)
	api.HandleFunc("POST /api/tailscale/logout", s.handleTailscaleLogout)
	api.HandleFunc("GET /api/tailscale/peers", s.handleTailscalePeers)
	api.HandleFunc("GET /api/tailscale/routes", s.handleTailscaleRoutesGet)
	api.HandleFunc("PUT /api/tailscale/routes", s.handleTailscaleRoutesPut)

	api.HandleFunc("GET /api/serial/ports", s.handleSerialPorts)
	api.HandleFunc("GET /api/bridges", s.handleBridgesGet)
	api.HandleFunc("PUT /api/bridges", s.handleBridgesPut)
	api.HandleFunc("POST /api/bridges/{id}/{action}", s.handleBridgeAction)

	api.HandleFunc("POST /api/ui/show", s.handleUIShow)
	api.HandleFunc("GET /api/deploy/status", s.handleDeployStatus)
	api.HandleFunc("POST /api/deploy/{action}", s.handleDeployAction)

	api.HandleFunc("GET /ws/events", s.handleEvents)
	api.HandleFunc("GET /ws/term/{id}", s.handleTerminal)

	s.mux.Handle("/api/", s.authMiddleware(api))
	s.mux.Handle("/ws/", s.authMiddleware(api))
	s.mux.Handle("/", s.authMiddleware(staticHandler()))
}

// StatusResponse 是 GET /api/status 与事件推送共用的总览结构。
type StatusResponse struct {
	Version       string           `json:"version"`
	Commit        string           `json:"commit"`
	Mode          string           `json:"mode"`
	UptimeSec     int64            `json:"uptimeSec"`
	DataDir       string           `json:"dataDir"`
	Listen        string           `json:"listen"`
	Source        Source           `json:"source,omitempty"`
	Identity      *tsnode.Identity `json:"identity,omitempty"`
	AccessWarning bool             `json:"accessWarning"` // 白名单为空
	Tailscale     tsnode.Snapshot  `json:"tailscale"`
	WebPort       int              `json:"webPort"`
	Bridges       []bridge.Status  `json:"bridges"`
}

func (s *Server) buildStatus(r *http.Request) StatusResponse {
	resp := StatusResponse{
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		Mode:          s.deps.Mode,
		UptimeSec:     int64(time.Since(s.deps.StartedAt).Seconds()),
		DataDir:       s.deps.Paths.DataDir,
		Listen:        s.deps.Config.Listen,
		AccessWarning: len(s.deps.Config.Access.AllowedLogins) == 0,
		WebPort:       s.deps.Config.Tailscale.WebPort,
		Bridges:       []bridge.Status{},
	}
	if r != nil {
		resp.Source = SourceOf(r)
		resp.Identity = IdentityOf(r)
	}
	if s.deps.Node != nil {
		resp.Tailscale = s.deps.Node.Snapshot()
	}
	if s.deps.Bridges != nil {
		resp.Bridges = s.deps.Bridges.Status()
	}
	return resp
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildStatus(r))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": buildinfo.Version,
		"commit":  buildinfo.Commit,
		"date":    buildinfo.Date,
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 500
	if v := r.URL.Query().Get("n"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 && i <= 5000 {
			n = i
		}
	}
	level := slog.LevelDebug
	if v := r.URL.Query().Get("level"); v != "" {
		if l, err := logging.ParseLevel(v); err == nil {
			level = l
		}
	}
	entries := s.deps.Ring.Last(n, level)
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleUIShow 由第二个实例调用，唤起本进程的窗口。只接受本机来源。
func (s *Server) handleUIShow(w http.ResponseWriter, r *http.Request) {
	if SourceOf(r) != SourceLocal {
		writeError(w, http.StatusForbidden, "仅限本机")
		return
	}
	if s.deps.ShowUI == nil {
		writeError(w, http.StatusNotFound, "本进程没有桌面壳")
		return
	}
	s.deps.ShowUI()
	writeJSON(w, http.StatusOK, map[string]string{"result": "shown"})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, _ *http.Request) {
	// 配置里没有密钥，可以整体返回。auth key 不落盘。
	writeJSON(w, http.StatusOK, s.deps.Config)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
