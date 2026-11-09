package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"rsagent/internal/config"
	"rsagent/internal/i18n"
	"rsagent/internal/systs"
)

// sysJob 是一次在后台执行的系统 tailscale 操作（安装、启动、配置防火墙），输出供界面轮询显示。
type sysJob struct {
	mu       sync.Mutex
	Action   string    `json:"action"`
	Running  bool      `json:"running"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Log      string    `json:"log"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
}

const sysJobLogLimit = 64 << 10

// Write 追加输出，只保留末尾一段。
func (j *sysJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Log += string(p)
	if len(j.Log) > sysJobLogLimit {
		j.Log = j.Log[len(j.Log)-sysJobLogLimit:]
	}
	return len(p), nil
}

func (j *sysJob) snapshot() sysJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return sysJob{Action: j.Action, Running: j.Running, OK: j.OK, Error: j.Error, Log: j.Log, Started: j.Started, Finished: j.Finished}
}

var sysActions = map[string]func(context.Context, *sysJob) error{
	"install": func(ctx context.Context, j *sysJob) error { return systs.Install(ctx, j) },
	"start": func(ctx context.Context, j *sysJob) error {
		if err := systs.StartService(ctx, j); err != nil {
			return err
		}
		return systs.EnsureFirewall(ctx, j)
	},
	"firewall": func(ctx context.Context, j *sysJob) error { return systs.EnsureFirewall(ctx, j) },
}

// POST /api/tailscale/system/{action}：install | start | firewall，后台执行，用 GET .../system/job 查看进度。
func (s *Server) handleSystemAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	fn, ok := sysActions[action]
	if !ok {
		writeError(w, http.StatusNotFound, i18n.T("未知动作"))
		return
	}
	s.sysMu.Lock()
	if s.sysJob != nil && s.sysJob.snapshot().Running {
		s.sysMu.Unlock()
		writeError(w, http.StatusConflict, i18n.T("已有操作在进行中"))
		return
	}
	job := &sysJob{Action: action, Running: true, Started: time.Now()}
	s.sysJob = job
	s.sysMu.Unlock()
	s.deps.Log.Info(i18n.T("开始系统 Tailscale 操作"), "action", action, "by", requesterLabel(r))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := fn(ctx, job)
		job.mu.Lock()
		job.Running = false
		job.Finished = time.Now()
		job.OK = err == nil
		if err != nil {
			job.Error = err.Error()
		}
		job.mu.Unlock()
		if err != nil {
			s.deps.Log.Error(i18n.T("系统 Tailscale 操作失败"), "action", action, "err", err)
		} else {
			s.deps.Log.Info(i18n.T("系统 Tailscale 操作完成"), "action", action)
		}
		if s.deps.Node != nil {
			s.deps.Node.RefreshSystem()
		}
		s.hub.Notify()
	}()
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// GET /api/tailscale/system/job
func (s *Server) handleSystemJob(w http.ResponseWriter, _ *http.Request) {
	s.sysMu.Lock()
	job := s.sysJob
	s.sysMu.Unlock()
	if job == nil {
		writeJSON(w, http.StatusOK, sysJob{})
		return
	}
	writeJSON(w, http.StatusOK, job.snapshot())
}

// POST /api/tailscale/login-interactive：在浏览器中登录，认证链接随状态推送到界面。
func (s *Server) handleTailscaleLoginInteractive(w http.ResponseWriter, r *http.Request) {
	// 改动 tailscaled 的设置要做完：客户端中途离开（关掉页面）也不取消
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	if err := s.deps.Node.LoginInteractive(ctx); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.deps.Log.Info(i18n.T("已发起浏览器登录"), "by", requesterLabel(r))
	writeJSON(w, http.StatusAccepted, s.deps.Node.Snapshot())
}

// PUT /api/tailscale/backend {"backend":"system"|"embedded"}：切换网络后端并保存。
func (s *Server) handleTailscaleBackend(w http.ResponseWriter, r *http.Request) {
	if SourceOf(r) == SourceTailnet {
		writeError(w, http.StatusForbidden, i18n.T("从 tailnet 侧切换网络后端会切断你自己的连接，请在远端本机操作"))
		return
	}
	var req struct {
		Backend string `json:"backend"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, i18n.T("请求体格式错误"))
		return
	}
	req.Backend = strings.TrimSpace(req.Backend)
	if req.Backend != config.BackendSystem && req.Backend != config.BackendEmbedded {
		writeError(w, http.StatusBadRequest, i18n.Sprintf("未知的网络后端 %q", req.Backend))
		return
	}
	if err := s.deps.Node.SetBackend(context.WithoutCancel(r.Context()), req.Backend); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.deps.Config.Tailscale.Backend = req.Backend
	if err := s.deps.Config.Save(s.deps.Paths.ConfigFile); err != nil {
		writeError(w, http.StatusInternalServerError, i18n.Sprintf("保存配置失败: %v", err))
		return
	}
	s.deps.Log.Info(i18n.T("已切换网络后端"), "backend", req.Backend, "by", requesterLabel(r))
	s.hub.Notify()
	writeJSON(w, http.StatusOK, s.deps.Node.Snapshot())
}
