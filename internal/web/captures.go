package web

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 录制文件放在日志目录，文件名形如 capture-<桥接id>-<时间>.rx.bin / .tx.bin。
var captureNameRe = regexp.MustCompile(`^capture-[A-Za-z0-9_-]+-\d{8}-\d{6}\.(rx|tx)\.bin$`)

type captureFile struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

func (s *Server) captureDir() string { return s.deps.Paths.LogDir }

// POST /api/bridges/{id}/capture/start
func (s *Server) handleCaptureStart(w http.ResponseWriter, r *http.Request) {
	b, ok := s.deps.Bridges.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "桥接不存在")
		return
	}
	base, err := b.StartCapture(s.captureDir())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.deps.Log.Info("录制已开始", "bridge", b.Status().ID, "base", base, "by", requesterLabel(r))
	writeJSON(w, http.StatusOK, map[string]any{"base": base, "capture": b.Capture()})
}

// POST /api/bridges/{id}/capture/stop
func (s *Server) handleCaptureStop(w http.ResponseWriter, r *http.Request) {
	b, ok := s.deps.Bridges.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "桥接不存在")
		return
	}
	if err := b.StopCapture(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"capture": b.Capture()})
}

// GET /api/captures
func (s *Server) handleCapturesList(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(s.captureDir())
	if err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	files := []captureFile{}
	for _, e := range entries {
		if e.IsDir() || !captureNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, captureFile{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime.After(files[j].ModTime) })
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) capturePath(name string) (string, bool) {
	if !captureNameRe.MatchString(name) || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	return filepath.Join(s.captureDir(), name), true
}

// GET /api/captures/{name}
func (s *Server) handleCaptureDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.capturePath(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusBadRequest, "文件名非法")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f)
}

// DELETE /api/captures/{name}
func (s *Server) handleCaptureDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.capturePath(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusBadRequest, "文件名非法")
		return
	}
	if err := os.Remove(p); err != nil {
		writeError(w, http.StatusNotFound, "删除失败: "+err.Error())
		return
	}
	s.deps.Log.Info("录制文件已删除", "name", filepath.Base(p), "by", requesterLabel(r))
	writeJSON(w, http.StatusOK, map[string]string{"result": "deleted"})
}
