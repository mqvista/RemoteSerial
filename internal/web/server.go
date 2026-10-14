// Package web 提供 HTTP 管理界面：REST API、WebSocket 推送和内嵌的前端静态文件。
//
// 同一个 handler 挂在两个监听上：本机 127.0.0.1 监听完全信任，
// tailnet 监听经过 WhoIs 白名单校验。请求来源通过 context 传递。
package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/config"
	"rsagent/internal/logging"
	"rsagent/internal/tsnode"
)

// Source 标记请求来自哪个监听。
type Source string

const (
	// SourceLocal 来自 127.0.0.1 引导监听。
	SourceLocal Source = "local"
	// SourceTailnet 来自 tsnet 监听。
	SourceTailnet Source = "tailnet"
)

type ctxKey int

const sourceKey ctxKey = iota

// SourceOf 返回请求来源。
func SourceOf(r *http.Request) Source {
	if s, ok := r.Context().Value(sourceKey).(Source); ok {
		return s
	}
	return ""
}

// Deps 是 web 层依赖的运行时对象。
type Deps struct {
	Log         *slog.Logger
	Ring        *logging.Ring
	Config      *config.Config
	Paths       config.Paths
	Node        *tsnode.Node
	Bridges     *bridge.Manager
	Hub         *Hub
	ShowUI      func() // 非空表示本进程带桌面壳，POST /api/ui/show 会唤起窗口
	SetLogLevel func(string) error
	Handoff     func() // 安装服务成功后停止本进程 core，standalone 模式才有
	StartedAt   time.Time
	Mode        string // standalone | service | shell
}

// Server 是 HTTP 管理界面。
type Server struct {
	deps Deps
	mux  *http.ServeMux
	hub  *Hub
}

// New 构造 Server 并注册路由。
func New(d Deps) *Server {
	if d.Hub == nil {
		d.Hub = NewHub()
	}
	s := &Server{deps: d, mux: http.NewServeMux(), hub: d.Hub}
	s.routes()
	return s
}

// Notify 触发一次状态推送。
func (s *Server) Notify() { s.hub.Notify() }

// Handler 返回带来源标记的根 handler。
func (s *Server) Handler(src Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), sourceKey, src)
		s.mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ListenAndServe 在 addr 上监听并阻塞到 ctx 取消。
func (s *Server) ListenAndServe(ctx context.Context, addr string, src Source) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln, src)
}

// Serve 用给定监听器提供服务，ctx 取消时优雅关闭。tsnet 的监听器也走这里。
func (s *Server) Serve(ctx context.Context, ln net.Listener, src Source) error {
	srv := &http.Server{
		Handler:           s.Handler(src),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ConnState: func(c net.Conn, st http.ConnState) {
			if st == http.StateNew && s.deps.Log != nil {
				s.deps.Log.Debug("HTTP 新连接", "source", src, "remote", c.RemoteAddr().String())
			}
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
