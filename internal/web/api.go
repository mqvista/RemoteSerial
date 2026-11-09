package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"rsagent/internal/i18n"
	"strconv"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/buildinfo"
	"rsagent/internal/logging"
	"rsagent/internal/platform"
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
	api.HandleFunc("POST /api/tailscale/ping", s.handleTailscalePing)
	api.HandleFunc("GET /api/tailscale/netcheck", s.handleTailscaleNetcheck)
	api.HandleFunc("POST /api/tailscale/login-interactive", s.handleTailscaleLoginInteractive)
	api.HandleFunc("PUT /api/tailscale/backend", s.handleTailscaleBackend)
	api.HandleFunc("POST /api/tailscale/system/{action}", s.handleSystemAction)
	api.HandleFunc("GET /api/tailscale/system/job", s.handleSystemJob)

	api.HandleFunc("POST /api/bridges/{id}/capture/start", s.handleCaptureStart)
	api.HandleFunc("POST /api/bridges/{id}/capture/stop", s.handleCaptureStop)
	api.HandleFunc("GET /api/captures", s.handleCapturesList)
	api.HandleFunc("GET /api/captures/{name}", s.handleCaptureDownload)
	api.HandleFunc("DELETE /api/captures/{name}", s.handleCaptureDelete)

	api.HandleFunc("GET /api/serial/ports", s.handleSerialPorts)
	api.HandleFunc("GET /api/bridges", s.handleBridgesGet)
	api.HandleFunc("PUT /api/bridges", s.handleBridgesPut)
	api.HandleFunc("POST /api/bridges/{id}/{action}", s.handleBridgeAction)

	api.HandleFunc("POST /api/ui/show", s.handleUIShow)
	api.HandleFunc("POST /api/ui/quit", s.handleUIQuit)
	api.HandleFunc("GET /api/update/status", s.handleUpdateStatus)
	api.HandleFunc("POST /api/update/check", s.handleUpdateCheck)
	api.HandleFunc("POST /api/update/download", s.handleUpdateDownload)
	api.HandleFunc("POST /api/update/cancel", s.handleUpdateCancel)
	api.HandleFunc("DELETE /api/update/downloaded", s.handleUpdateClear)
	api.HandleFunc("POST /api/update/apply", s.handleUpdateApply)
	api.HandleFunc("POST /api/update/auto", s.handleUpdateAuto)
	api.HandleFunc("GET /api/deploy/status", s.handleDeployStatus)
	api.HandleFunc("POST /api/deploy/{action}", s.handleDeployAction)

	api.HandleFunc("GET /api/session", s.handleSession)
	api.HandleFunc("POST /api/session/login", s.handleSessionLogin)
	api.HandleFunc("POST /api/session/logout", s.handleSessionLogout)
	api.HandleFunc("POST /api/session/token", s.handleSessionToken)
	api.HandleFunc("PUT /api/session/password", s.handleSessionPassword)
	api.HandleFunc("GET /api/console", s.handleConsoleGet)
	api.HandleFunc("PUT /api/console", s.handleConsolePut)

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
	Platform      string           `json:"platform"`                  // windows | darwin | linux | openwrt
	PlatformVer   string           `json:"platformVersion,omitempty"` // OpenWrt 版本
	UptimeSec     int64            `json:"uptimeSec"`
	DataDir       string           `json:"dataDir"`
	Listen        string           `json:"listen"`
	Source        Source           `json:"source,omitempty"`
	Identity      *tsnode.Identity `json:"identity,omitempty"`
	AccessWarning bool             `json:"accessWarning"` // 白名单为空
	Language      string           `json:"language"`      // 当前界面语言 en | zh-TW
	LanguageAuto  bool             `json:"languageAuto"`  // 配置未指定，跟随系统
	Tailscale     tsnode.Snapshot  `json:"tailscale"`
	WebPort       int              `json:"webPort"`
	Bridges       []bridge.Status  `json:"bridges"`
	Update        UpdateSummary    `json:"update"`
}

// UpdateSummary 是总览里的更新提示。
type UpdateSummary struct {
	Available  bool   `json:"available"`
	Latest     string `json:"latest,omitempty"`
	Downloaded string `json:"downloaded,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s *Server) buildStatus(r *http.Request) StatusResponse {
	resp := StatusResponse{
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		Mode:          s.deps.Mode,
		Platform:      platform.Name(),
		PlatformVer:   platform.OpenWrtRelease(),
		UptimeSec:     int64(time.Since(s.deps.StartedAt).Seconds()),
		DataDir:       s.deps.Paths.DataDir,
		Listen:        s.deps.Config.Listen,
		AccessWarning: len(s.deps.Config.Access.AllowedLogins) == 0,
		Language:      string(i18n.Current()),
		LanguageAuto:  s.deps.Config.Language == "",
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
	if s.deps.Updater != nil {
		st := s.deps.Updater.Status()
		resp.Update.Available = st.Available
		resp.Update.Error = st.Error
		if st.Latest != nil {
			resp.Update.Latest = st.Latest.Version
		}
		if st.Downloaded != nil {
			resp.Update.Downloaded = st.Downloaded.Version
		}
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
		writeError(w, http.StatusForbidden, i18n.T("仅限本机"))
		return
	}
	if s.deps.ShowUI == nil {
		writeError(w, http.StatusNotFound, i18n.T("本进程没有桌面壳"))
		return
	}
	s.deps.ShowUI()
	writeJSON(w, http.StatusOK, map[string]string{"result": "shown"})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, _ *http.Request) {
	// 配置里只有局域网密码的哈希算敏感项，去掉后整体返回。auth key 不落盘。
	c := *s.deps.Config
	c.Access.PasswordHash = ""
	writeJSON(w, http.StatusOK, &c)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
