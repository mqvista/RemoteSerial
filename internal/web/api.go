package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"rsagent/internal/buildinfo"
	"rsagent/internal/logging"
)

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.mux.HandleFunc("GET /api/logs", s.handleLogs)
	s.mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	s.mux.Handle("/", staticHandler())
}

// StatusResponse 是 GET /api/status 的返回。
type StatusResponse struct {
	Version   string          `json:"version"`
	Commit    string          `json:"commit"`
	Mode      string          `json:"mode"`
	UptimeSec int64           `json:"uptimeSec"`
	DataDir   string          `json:"dataDir"`
	Listen    string          `json:"listen"`
	Source    Source          `json:"source"`
	Tailscale TailscaleStatus `json:"tailscale"`
	Bridges   []BridgeStatus  `json:"bridges"`
}

// TailscaleStatus 是节点状态摘要。第 0 期只有 NotConfigured。
type TailscaleStatus struct {
	State    string `json:"state"` // NotConfigured | NeedsLogin | Starting | NeedsMachineAuth | Running | Stopped
	Hostname string `json:"hostname"`
	IP       string `json:"ip,omitempty"`
	DNSName  string `json:"dnsName,omitempty"`
}

// BridgeStatus 是单个桥接的运行状态。第 0 期为空列表。
type BridgeStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Port      string `json:"port"`
	TCPPort   int    `json:"tcpPort"`
	State     string `json:"state"`
	Client    string `json:"client,omitempty"`
	BytesIn   int64  `json:"bytesIn"`
	BytesOut  int64  `json:"bytesOut"`
	LastError string `json:"lastError,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		Version:   buildinfo.Version,
		Commit:    buildinfo.Commit,
		Mode:      s.deps.Mode,
		UptimeSec: int64(time.Since(s.deps.StartedAt).Seconds()),
		DataDir:   s.deps.Paths.DataDir,
		Listen:    s.deps.Config.Listen,
		Source:    SourceOf(r),
		Tailscale: TailscaleStatus{
			State:    "NotConfigured",
			Hostname: s.deps.Config.Tailscale.Hostname,
		},
		Bridges: []BridgeStatus{},
	}
	for _, b := range s.deps.Config.Bridges {
		resp.Bridges = append(resp.Bridges, BridgeStatus{
			ID: b.ID, Name: b.Name, Port: b.Port, TCPPort: b.TCPPort, State: "Stopped",
		})
	}
	writeJSON(w, http.StatusOK, resp)
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
