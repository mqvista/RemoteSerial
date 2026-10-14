package web

import (
	"context"
	"net/http"
	"strings"

	"rsagent/internal/tsnode"
)

const identityKey ctxKey = iota + 10

// IdentityOf 返回 tailnet 请求方身份，本机请求返回 nil。
func IdentityOf(r *http.Request) *tsnode.Identity {
	id, _ := r.Context().Value(identityKey).(*tsnode.Identity)
	return id
}

// authMiddleware 对 tailnet 来源做 WhoIs 校验与白名单检查，本机来源直接放行。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SourceOf(r) != SourceTailnet {
			next.ServeHTTP(w, r)
			return
		}
		if s.deps.Node == nil {
			http.Error(w, "节点未初始化", http.StatusServiceUnavailable)
			return
		}
		id, err := s.deps.Node.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			s.deps.Log.Warn("WhoIs 失败，拒绝访问", "remote", r.RemoteAddr, "err", err)
			http.Error(w, "无法识别请求方身份", http.StatusForbidden)
			return
		}
		if !s.loginAllowed(id.LoginName) {
			s.deps.Log.Warn("登录名不在白名单，拒绝访问", "login", id.LoginName, "node", id.Node, "path", r.URL.Path)
			http.Error(w, "此账号无权访问", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) loginAllowed(login string) bool {
	allowed := s.deps.Config.Access.AllowedLogins
	if len(allowed) == 0 {
		return true
	}
	login = strings.ToLower(strings.TrimSpace(login))
	for _, a := range allowed {
		if strings.ToLower(strings.TrimSpace(a)) == login {
			return true
		}
	}
	return false
}
