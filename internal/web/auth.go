package web

import (
	"context"
	"net/http"
	"rsagent/internal/i18n"
	"strings"

	"rsagent/internal/tsnode"
)

const identityKey ctxKey = iota + 10

// IdentityOf 返回 tailnet 请求方身份，本机请求返回 nil。
func IdentityOf(r *http.Request) *tsnode.Identity {
	id, _ := r.Context().Value(identityKey).(*tsnode.Identity)
	return id
}

// authMiddleware 对 tailnet 来源做 WhoIs 校验与白名单检查，局域网来源要求登录，本机来源直接放行。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SourceOf(r) == SourceLAN {
			if s.lanGate(w, r) {
				next.ServeHTTP(w, r)
			}
			return
		}
		if SourceOf(r) != SourceTailnet {
			// 本机来源也处理 LuCI 跳转带来的令牌，去掉地址里的令牌
			if s.redeemFromQuery(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if s.deps.Node == nil {
			http.Error(w, i18n.T("节点未初始化"), http.StatusServiceUnavailable)
			return
		}
		id, err := s.deps.Node.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			s.deps.Log.Warn(i18n.T("WhoIs 失败，拒绝访问"), "remote", r.RemoteAddr, "err", err)
			http.Error(w, i18n.T("无法识别请求方身份"), http.StatusForbidden)
			return
		}
		if !s.loginAllowed(id.LoginName) {
			s.deps.Log.Warn(i18n.T("登录名不在白名单，拒绝访问"), "login", id.LoginName, "node", id.Node, "path", r.URL.Path)
			http.Error(w, i18n.T("此账号无权访问"), http.StatusForbidden)
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
