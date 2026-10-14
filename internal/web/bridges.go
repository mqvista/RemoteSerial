package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"rsagent/internal/bridge"
	"rsagent/internal/config"
)

// GET /api/serial/ports
func (s *Server) handleSerialPorts(w http.ResponseWriter, _ *http.Request) {
	ports, err := bridge.ListPorts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "枚举串口失败: "+err.Error())
		return
	}
	if s.deps.Bridges != nil {
		used := s.deps.Bridges.UsedPorts()
		for i := range ports {
			ports[i].UsedBy = used[ports[i].Name]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ports": ports})
}

type bridgeView struct {
	Config config.Bridge  `json:"config"`
	Status *bridge.Status `json:"status,omitempty"`
}

// GET /api/bridges
func (s *Server) handleBridgesGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"bridges": s.bridgeViews()})
}

func (s *Server) bridgeViews() []bridgeView {
	statuses := map[string]bridge.Status{}
	if s.deps.Bridges != nil {
		for _, st := range s.deps.Bridges.Status() {
			statuses[st.ID] = st
		}
	}
	out := make([]bridgeView, 0, len(s.deps.Config.Bridges))
	for _, c := range s.deps.Config.Bridges {
		v := bridgeView{Config: c}
		if st, ok := statuses[c.ID]; ok {
			stCopy := st
			v.Status = &stCopy
		}
		out = append(out, v)
	}
	return out
}

// PUT /api/bridges {"bridges": [...]}：整体替换桥接配置。
func (s *Server) handleBridgesPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Bridges []config.Bridge `json:"bridges"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误: "+err.Error())
		return
	}
	for i := range req.Bridges {
		normalizeBridge(&req.Bridges[i])
	}
	old := s.deps.Config.Bridges
	s.deps.Config.Bridges = req.Bridges
	if err := s.deps.Config.Validate(); err != nil {
		s.deps.Config.Bridges = old
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.deps.Config.Save(s.deps.Paths.ConfigFile); err != nil {
		s.deps.Config.Bridges = old
		writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	if s.deps.Bridges != nil {
		s.deps.Bridges.Apply(s.deps.Config.Bridges)
	}
	s.deps.Log.Info("桥接配置已更新", "count", len(req.Bridges), "by", requesterLabel(r))
	writeJSON(w, http.StatusOK, map[string]any{"bridges": s.bridgeViews()})
}

// POST /api/bridges/{id}/start | stop | kick
func (s *Server) handleBridgeAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")
	if s.deps.Bridges == nil {
		writeError(w, http.StatusServiceUnavailable, "桥接模块未初始化")
		return
	}
	idx := -1
	for i, b := range s.deps.Config.Bridges {
		if b.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeError(w, http.StatusNotFound, "桥接不存在")
		return
	}
	var err error
	switch action {
	case "start", "stop":
		enabled := action == "start"
		s.deps.Config.Bridges[idx].Enabled = enabled
		if serr := s.deps.Config.Save(s.deps.Paths.ConfigFile); serr != nil {
			s.deps.Log.Error("保存配置失败", "err", serr)
		}
		err = s.deps.Bridges.SetEnabled(id, enabled)
		s.deps.Log.Info("桥接"+map[bool]string{true: "启动", false: "停止"}[enabled], "id", id, "by", requesterLabel(r))
	case "kick":
		err = s.deps.Bridges.Kick(id)
	default:
		writeError(w, http.StatusNotFound, "未知操作")
		return
	}
	resp := map[string]any{"bridges": s.bridgeViews()}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func normalizeBridge(b *config.Bridge) {
	b.ID = strings.TrimSpace(b.ID)
	b.Name = strings.TrimSpace(b.Name)
	b.Port = strings.TrimSpace(b.Port)
	b.Parity = strings.ToUpper(strings.TrimSpace(b.Parity))
	if b.Parity == "" {
		b.Parity = "N"
	}
	if b.DataBits == 0 {
		b.DataBits = 8
	}
	if b.StopBits == 0 {
		b.StopBits = 1
	}
	if b.Mode == "" {
		b.Mode = "raw"
	}
	if b.ClientPolicy == "" {
		b.ClientPolicy = "replace"
	}
	if b.Match != nil && b.Match.VID == "" && b.Match.PID == "" && b.Match.Serial == "" {
		b.Match = nil
	}
}
