package web

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"rsagent/internal/deploy"
)

func (s *Server) deployOptions() deploy.Options {
	exe, err := os.Executable()
	if err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
	}
	return deploy.Options{
		ExePath: exe,
		DataDir: s.deps.Paths.DataDir,
		Listen:  s.deps.Config.Listen,
		UDPPort: s.deps.Config.Tailscale.UDPPort,
		Log:     s.deps.Log,
	}
}

type deployStatusResponse struct {
	deploy.Status
	Mode     string `json:"mode"`
	HasShell bool   `json:"hasShell"`
}

// GET /api/deploy/status
func (s *Server) handleDeployStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, deployStatusResponse{
		Status:   deploy.GetStatus(s.deployOptions()),
		Mode:     s.deps.Mode,
		HasShell: s.deps.ShowUI != nil,
	})
}

// POST /api/deploy/{action}
func (s *Server) handleDeployAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if !deploy.ValidAction(action) {
		writeError(w, http.StatusNotFound, "未知动作")
		return
	}
	if s.deps.Mode == "service" {
		switch action {
		case deploy.ActionServiceRemove, deploy.ActionServiceStop:
			writeError(w, http.StatusConflict, "当前就运行在服务里，请在远端本机用托盘程序或命令行操作")
			return
		}
	}
	s.deps.Log.Info("执行部署动作", "action", action, "by", requesterLabel(r))
	res := deploy.Execute(s.deployOptions(), action)
	if res.OK {
		s.deps.Log.Info("部署动作完成", "action", action)
	} else {
		s.deps.Log.Warn("部署动作失败", "action", action, "msg", res.Message)
	}

	// 服务装好后，本进程的 core 让位给服务；壳继续运行并自动连上服务。
	if res.OK && action == deploy.ActionServiceInstall && s.deps.Mode == "standalone" && s.deps.Handoff != nil {
		res.Message = "服务已安装并启动。本进程的 core 将在 2 秒后停止，托盘与窗口会自动连接到服务。"
		go func() {
			time.Sleep(2 * time.Second)
			s.deps.Handoff()
		}()
	}
	code := http.StatusOK
	if !res.OK {
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]any{
		"result": res,
		"status": deploy.GetStatus(s.deployOptions()),
	})
}
