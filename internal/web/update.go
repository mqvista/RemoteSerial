package web

import (
	"context"
	"net/http"
	"time"

	"rsagent/internal/deploy"
)

// GET /api/update/status
func (s *Server) handleUpdateStatus(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Updater == nil {
		writeError(w, http.StatusNotFound, "本进程不支持更新")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Updater.Status())
}

// POST /api/update/check
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.deps.Updater == nil {
		writeError(w, http.StatusNotFound, "本进程不支持更新")
		return
	}
	st, err := s.deps.Updater.Check(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "status": st})
		return
	}
	s.deps.Log.Info("检查更新", "current", st.Current, "latest", st.Latest.Version, "available", st.Available, "by", requesterLabel(r))
	writeJSON(w, http.StatusOK, st)
}

// POST /api/update/download：后台下载，进度在 status 里。
func (s *Server) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Updater == nil {
		writeError(w, http.StatusNotFound, "本进程不支持更新")
		return
	}
	// 下载不绑定请求的 context，请求结束后继续。
	if err := s.deps.Updater.StartDownload(context.Background()); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.deps.Log.Info("开始下载更新", "by", requesterLabel(r))
	writeJSON(w, http.StatusAccepted, s.deps.Updater.Status())
}

// POST /api/update/cancel
func (s *Server) handleUpdateCancel(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Updater == nil {
		writeError(w, http.StatusNotFound, "本进程不支持更新")
		return
	}
	s.deps.Updater.CancelDownload()
	writeJSON(w, http.StatusOK, s.deps.Updater.Status())
}

// DELETE /api/update/downloaded：删除已下载的程序。
func (s *Server) handleUpdateClear(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Updater == nil {
		writeError(w, http.StatusNotFound, "本进程不支持更新")
		return
	}
	s.deps.Updater.ClearPending()
	writeJSON(w, http.StatusOK, s.deps.Updater.Status())
}

// POST /api/update/apply：用已下载的程序替换当前程序并重启。
//
// 单进程模式：需要时提权替换文件，然后本进程重启。
// 服务模式：服务进程以 SYSTEM 运行，直接替换；重启服务交给独立子进程，
// 远端托盘程序发现 core 版本变化后会自行替换并重启。
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.deps.Updater == nil {
		writeError(w, http.StatusNotFound, "本进程不支持更新")
		return
	}
	st := s.deps.Updater.Status()
	if st.Downloaded == nil {
		writeError(w, http.StatusBadRequest, "没有已下载的更新，请先下载")
		return
	}
	if st.Downloading {
		writeError(w, http.StatusConflict, "正在下载，请稍候")
		return
	}
	opts := s.deployOptions()
	by := requesterLabel(r)
	s.deps.Log.Info("应用更新", "version", st.Downloaded.Version, "mode", s.deps.Mode, "by", by)

	if s.deps.Mode == "service" {
		if err := deploy.Run(opts, deploy.ActionUpdateApply); err != nil {
			s.deps.Log.Warn("应用更新失败", "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := deploy.ExecuteDetached(opts, deploy.ActionServiceRestart); err != nil {
			writeError(w, http.StatusInternalServerError, "程序已替换，但重启服务失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"result": deploy.Result{Action: deploy.ActionUpdateApply, OK: true, Time: time.Now(),
				Message: "程序已替换，服务将在几秒内重启到新版本。远端托盘程序会自动跟随更新并重启，界面稍后自动恢复。"},
		})
		return
	}

	res := deploy.Execute(opts, deploy.ActionUpdateApply)
	if !res.OK {
		s.deps.Log.Warn("应用更新失败", "msg", res.Message)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": res.Message, "result": res})
		return
	}
	if s.deps.Restart != nil {
		res.Message = "程序已替换，2 秒后自动重启到新版本，界面稍后自动恢复。"
		go func() {
			time.Sleep(2 * time.Second)
			s.deps.Restart()
		}()
	} else {
		res.Message = "程序已替换，请重新启动 rsagent。"
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

// POST /api/ui/quit：退出本进程（托盘与本进程内的 core）。只接受本机来源，供安装程序和命令行使用。
func (s *Server) handleUIQuit(w http.ResponseWriter, r *http.Request) {
	if SourceOf(r) != SourceLocal {
		writeError(w, http.StatusForbidden, "仅限本机")
		return
	}
	if s.deps.Quit == nil {
		writeError(w, http.StatusNotFound, "本进程不支持远程退出")
		return
	}
	s.deps.Log.Info("收到退出请求", "mode", s.deps.Mode)
	writeJSON(w, http.StatusOK, map[string]string{"result": "quitting"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.deps.Quit()
	}()
}
