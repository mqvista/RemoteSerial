package web

// 局域网访问：本机监听收到的非回环请求视为局域网来源，必须先登录。
// 登录方式有两种：LuCI 用 rsagent ctl token 拿到的一次性令牌（?rsagent_token=…），或局域网密码。
// 会话只保存在内存里，进程重启后需要重新登录。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"rsagent/internal/i18n"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "rsagent_session"
	tokenParam    = "rsagent_token"
	tokenTTL      = 60 * time.Second
	sessionTTL    = 12 * time.Hour
	minPassword   = 8
)

var (
	tailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// sessions 保存一次性令牌与登录会话。
type sessions struct {
	mu     sync.Mutex
	tokens map[string]time.Time // 一次性令牌 → 过期时间
	active map[string]time.Time // 会话 → 过期时间（每次使用顺延）
	fails  map[string][]time.Time
}

func newSessions() *sessions {
	return &sessions{tokens: map[string]time.Time{}, active: map[string]time.Time{}, fails: map[string][]time.Time{}}
}

func randomID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *sessions) gc(now time.Time) {
	for k, exp := range s.tokens {
		if now.After(exp) {
			delete(s.tokens, k)
		}
	}
	for k, exp := range s.active {
		if now.After(exp) {
			delete(s.active, k)
		}
	}
}

// newToken 生成 60 秒内有效、只能使用一次的令牌。
func (s *sessions) newToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.gc(now)
	t := randomID()
	s.tokens[t] = now.Add(tokenTTL)
	return t
}

// redeem 用掉令牌，成功时返回新会话。
func (s *sessions) redeem(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.gc(now)
	if _, ok := s.tokens[token]; !ok || token == "" {
		return "", false
	}
	delete(s.tokens, token)
	id := randomID()
	s.active[id] = now.Add(sessionTTL)
	return id, true
}

func (s *sessions) create() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := randomID()
	s.active[id] = time.Now().Add(sessionTTL)
	return id
}

func (s *sessions) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	exp, ok := s.active[id]
	if !ok || now.After(exp) {
		delete(s.active, id)
		return false
	}
	s.active[id] = now.Add(sessionTTL)
	return true
}

func (s *sessions) drop(id string) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

// dropAll 让所有会话失效（修改或清除密码时）。
func (s *sessions) dropAll() {
	s.mu.Lock()
	s.active = map[string]time.Time{}
	s.mu.Unlock()
}

// allowAttempt 限制每个地址每分钟最多 10 次失败的密码尝试。
func (s *sessions) allowAttempt(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	keep := s.fails[ip][:0]
	for _, t := range s.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	s.fails[ip] = keep
	return len(keep) < 10
}

func (s *sessions) fail(ip string) {
	s.mu.Lock()
	s.fails[ip] = append(s.fails[ip], time.Now())
	s.mu.Unlock()
}

func remoteIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		a, _ := netip.ParseAddr(host)
		return a.Unmap()
	}
	return ap.Addr().Unmap()
}

// classify 判断本机监听收到的请求来自哪里：回环是本机；系统 Tailscale 模式下 tailscale 地址段是 tailnet
// （随后照常做 WhoIs 校验）；其余都是局域网。
func (s *Server) classify(r *http.Request) Source {
	ip := remoteIP(r)
	if ip.IsLoopback() {
		return SourceLocal
	}
	if s.deps.Node != nil && s.deps.Node.System() && (tailscaleV4.Contains(ip) || tailscaleV6.Contains(ip)) {
		return SourceTailnet
	}
	return SourceLAN
}

func (s *Server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.sess.valid(c.Value)
}

func setSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
}

// sameOrigin 拒绝跨站的写请求与 WebSocket：浏览器跨站请求一定带 Origin。
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// redeemFromQuery 处理 ?rsagent_token=…：令牌有效就发会话 Cookie，然后跳转到去掉令牌的地址。
func (s *Server) redeemFromQuery(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	tok := q.Get(tokenParam)
	if tok == "" || r.Method != http.MethodGet {
		return false
	}
	q.Del(tokenParam)
	next := *r.URL
	next.RawQuery = q.Encode()
	if id, ok := s.sess.redeem(tok); ok {
		setSessionCookie(w, id)
		s.deps.Log.Info(i18n.T("已用一次性令牌登录管理界面"), "remote", r.RemoteAddr)
	} else {
		s.deps.Log.Warn(i18n.T("一次性令牌无效或已过期"), "remote", r.RemoteAddr)
	}
	http.Redirect(w, r, next.RequestURI(), http.StatusSeeOther)
	return true
}

// lanGate 对局域网来源做登录检查。返回 false 时已写好响应。
func (s *Server) lanGate(w http.ResponseWriter, r *http.Request) bool {
	if s.redeemFromQuery(w, r) {
		return false
	}
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || strings.HasPrefix(r.URL.Path, "/ws/") {
		if !sameOrigin(r) {
			writeError(w, http.StatusForbidden, i18n.T("拒绝跨站请求"))
			return false
		}
	}
	// 前端页面本身与登录接口不需要登录
	if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/ws/") {
		return true
	}
	switch r.URL.Path {
	case "/api/session", "/api/session/login":
		return true
	}
	if s.sessionOK(r) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": i18n.T("请先登录"), "login": true})
	return false
}

// SessionInfo 是 GET /api/session 的结果，前端据此决定是否显示登录页。
type SessionInfo struct {
	Source        Source `json:"source"`
	Authenticated bool   `json:"authenticated"`
	PasswordSet   bool   `json:"passwordSet"`
	LAN           bool   `json:"lan"`
}

// GET /api/session
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	src := SourceOf(r)
	writeJSON(w, http.StatusOK, SessionInfo{
		Source:        src,
		Authenticated: src != SourceLAN || s.sessionOK(r),
		PasswordSet:   s.deps.Config.Access.PasswordHash != "",
		LAN:           s.deps.Config.Access.LAN,
	})
}

// POST /api/session/login {"password": "..."}
func (s *Server) handleSessionLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, i18n.T("请求体格式错误"))
		return
	}
	hash := s.deps.Config.Access.PasswordHash
	if hash == "" {
		writeError(w, http.StatusForbidden, i18n.T("尚未设置局域网密码，请从 LuCI 的“打开管理界面”进入"))
		return
	}
	ip := remoteIP(r).String()
	if !s.sess.allowAttempt(ip) {
		writeError(w, http.StatusTooManyRequests, i18n.T("密码错误次数过多，请一分钟后再试"))
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		s.sess.fail(ip)
		time.Sleep(time.Second)
		s.deps.Log.Warn(i18n.T("局域网登录密码错误"), "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, i18n.T("密码错误"))
		return
	}
	setSessionCookie(w, s.sess.create())
	s.deps.Log.Info(i18n.T("已用密码登录管理界面"), "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// POST /api/session/logout
func (s *Server) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sess.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

// POST /api/session/token：只接受本机来源（rpcd 插件经 rsagent ctl 调用）。
func (s *Server) handleSessionToken(w http.ResponseWriter, r *http.Request) {
	if SourceOf(r) != SourceLocal {
		writeError(w, http.StatusForbidden, i18n.T("仅限本机"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     s.sess.newToken(),
		"param":     tokenParam,
		"expiresIn": int(tokenTTL.Seconds()),
		"port":      s.deps.Config.ConsolePort(),
		"lan":       s.deps.Config.Access.LAN,
	})
}

// PUT /api/session/password {"password": "..."}：设置局域网密码，空字符串表示清除。所有会话随之失效。
func (s *Server) handleSessionPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, i18n.T("请求体格式错误"))
		return
	}
	hash := ""
	if req.Password != "" {
		if len(req.Password) < minPassword {
			writeError(w, http.StatusBadRequest, i18n.Sprintf("密码至少 %d 个字符", minPassword))
			return
		}
		if len(req.Password) > 72 {
			writeError(w, http.StatusBadRequest, i18n.T("密码不能超过 72 个字节"))
			return
		}
		b, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		hash = string(b)
	}
	old := s.deps.Config.Access.PasswordHash
	s.deps.Config.Access.PasswordHash = hash
	if err := s.deps.Config.Save(s.deps.Paths.ConfigFile); err != nil {
		s.deps.Config.Access.PasswordHash = old
		writeError(w, http.StatusInternalServerError, i18n.Sprintf("保存配置失败: %v", err))
		return
	}
	s.sess.dropAll()
	// 自己在局域网上改密码时保持登录
	if SourceOf(r) == SourceLAN && hash != "" {
		setSessionCookie(w, s.sess.create())
	}
	s.deps.Log.Info(i18n.T("已更新局域网密码"), "set", hash != "", "by", requesterLabel(r))
	writeJSON(w, http.StatusOK, map[string]bool{"passwordSet": hash != ""})
}
