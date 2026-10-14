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
	opts := s.deployOptions()
	by := requesterLabel(r)

	if s.deps.Mode == "service" {
		switch action {
		case deploy.ActionTaskInstall, deploy.ActionTaskRemove, deploy.ActionWebView2Install:
			// 服务跑在 SYSTEM 账户与会话 0，计划任务会注册到错误的用户，安装器界面也看不见。
			writeError(w, http.StatusConflict, "服务模式下此动作需要用户会话，请在远端本机命令行执行: rsagent deploy "+action)
			return
		case deploy.ActionServiceRemove, deploy.ActionServiceStop:
			// 动作会结束本进程，交给独立子进程执行，先回复界面。
			s.deps.Log.Info("服务将由子进程"+map[string]string{deploy.ActionServiceRemove: "卸载", deploy.ActionServiceStop: "停止"}[action], "by", by)
			if err := deploy.ExecuteDetached(opts, action); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			msg := "服务将在几秒内停止。"
			if action == deploy.ActionServiceRemove {
				msg = "服务将在几秒内停止并卸载。"
			}
			msg += "远端托盘程序会自动在本进程重新启动 core，界面稍后自动恢复。"
			writeJSON(w, http.StatusAccepted, map[string]any{
				"result": deploy.Result{Action: action, OK: true, Message: msg, Time: time.Now()},
				"status": deploy.GetStatus(opts),
			})
			return
		}
	}

	s.deps.Log.Info("执行部署动作", "action", action, "by", by)
	res := deploy.Execute(opts, action)
	if res.OK {
		s.deps.Log.Info("部署动作完成", "action", action)
	} else {
		s.deps.Log.Warn("部署动作失败", "action", action, "msg", res.Message)
	}

	// 服务起来后，本进程的 core 让位给服务；壳继续运行并自动连上服务。
	if res.OK && (action == deploy.ActionServiceInstall || action == deploy.ActionServiceStart) &&
		s.deps.Mode == "standalone" && s.deps.Handoff != nil {
		res.Message = "服务已启动。本进程的 core 将在 2 秒后停止，托盘与窗口会自动连接到服务。"
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
		"status": deploy.GetStatus(opts),
	})
}
