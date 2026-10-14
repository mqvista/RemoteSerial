package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// 前端构建产物由 web/ 下的 Vite 输出到 internal/web/dist。
// dist/.keep 保证目录非空，未构建时也能通过编译，此时显示提示页。
//
//go:embed all:dist
var distFS embed.FS

const notBuiltPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>rsagent</title>
<style>body{font-family:system-ui,sans-serif;margin:3rem auto;max-width:40rem;line-height:1.6}code{background:#eee;padding:.1em .3em}</style>
</head><body>
<h1>rsagent 已运行</h1>
<p>前端尚未构建。在 <code>web/</code> 目录执行 <code>npm install</code> 和 <code>npm run build</code>，或直接运行仓库根目录的 <code>build.ps1</code>，然后重新编译。</p>
<p>API 可用：<a href="/api/status">/api/status</a>、<a href="/api/logs">/api/logs</a>、<a href="/api/settings">/api/settings</a>。</p>
</body></html>`

func staticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return notBuilt()
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return notBuilt()
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		// 单页应用：未知路径回落到 index.html，由前端路由处理。
		data, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
}

func notBuilt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(notBuiltPage))
	})
}
