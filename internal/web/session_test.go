package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rsagent/internal/config"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	dir := t.TempDir()
	return New(Deps{
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: cfg,
		Paths:  config.Paths{DataDir: dir, ConfigFile: filepath.Join(dir, "config.json")},
	})
}

func do(s *Server, method, target, remote string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler(SourceLocal).ServeHTTP(w, r)
	return w
}

const lanAddr = "192.168.1.20:50000"

func TestClassify(t *testing.T) {
	s := testServer(t)
	for remote, want := range map[string]Source{
		"127.0.0.1:1234":            SourceLocal,
		"[::1]:1234":                SourceLocal,
		"[::ffff:127.0.0.1]:1234":   SourceLocal,
		lanAddr:                     SourceLAN,
		"[::ffff:192.168.1.5]:1234": SourceLAN,
		"100.100.1.2:1234":          SourceLAN, // 没有系统 Tailscale 节点时不当作 tailnet
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if got := s.classify(r); got != want {
			t.Errorf("%s: got %s, want %s", remote, got, want)
		}
	}
}

func TestLANNeedsLogin(t *testing.T) {
	s := testServer(t)
	if w := do(s, "GET", "/api/status", lanAddr, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("LAN status without session: %d", w.Code)
	}
	if w := do(s, "GET", "/api/status", "127.0.0.1:1", "", nil); w.Code != http.StatusOK {
		t.Fatalf("local status: %d", w.Code)
	}
	if w := do(s, "GET", "/api/session", lanAddr, "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatalf("LAN session info: %d %s", w.Code, w.Body)
	}
	// 令牌只能由本机申请
	if w := do(s, "POST", "/api/session/token", lanAddr, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("LAN token request: %d", w.Code)
	}
}

func TestTokenRedeem(t *testing.T) {
	s := testServer(t)
	tok := s.sess.newToken()
	w := do(s, "GET", "/bridges?"+tokenParam+"="+tok+"&x=1", lanAddr, "", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/bridges?x=1" {
		t.Fatalf("redeem: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookie := w.Result().Cookies()
	if len(cookie) != 1 || cookie[0].Name != sessionCookie {
		t.Fatalf("cookie: %v", cookie)
	}
	hdr := map[string]string{"Cookie": sessionCookie + "=" + cookie[0].Value}
	if w := do(s, "GET", "/api/status", lanAddr, "", hdr); w.Code != http.StatusOK {
		t.Fatalf("with session: %d", w.Code)
	}
	// 令牌只能用一次
	w = do(s, "GET", "/?"+tokenParam+"="+tok, lanAddr, "", nil)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("token reused")
	}
	// 跨站写请求被拒绝
	hdr["Origin"] = "http://evil.example"
	if w := do(s, "PUT", "/api/settings", lanAddr, `{}`, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site PUT: %d", w.Code)
	}
}

func TestPasswordLogin(t *testing.T) {
	s := testServer(t)
	if w := do(s, "POST", "/api/session/login", lanAddr, `{"password":"x"}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("login without password set: %d", w.Code)
	}
	if w := do(s, "PUT", "/api/session/password", "127.0.0.1:1", `{"password":"short"}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d", w.Code)
	}
	if w := do(s, "PUT", "/api/session/password", "127.0.0.1:1", `{"password":"correct horse"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("set password: %d %s", w.Code, w.Body)
	}
	if w := do(s, "GET", "/api/settings", "127.0.0.1:1", "", nil); strings.Contains(w.Body.String(), "$2a$") {
		t.Fatal("settings leak the password hash")
	}
	w := do(s, "POST", "/api/session/login", lanAddr, `{"password":"correct horse"}`, nil)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", w.Code)
	}
	hdr := map[string]string{"Cookie": sessionCookie + "=" + w.Result().Cookies()[0].Value}
	if w := do(s, "GET", "/api/status", lanAddr, "", hdr); w.Code != http.StatusOK {
		t.Fatalf("after login: %d", w.Code)
	}
}

func TestConsolePortCheck(t *testing.T) {
	s := testServer(t)
	s.deps.Config.Bridges = []config.Bridge{{ID: "a", Name: "dev", TCPPort: 4001}}
	if err := s.checkConsolePort(4001); err == nil {
		t.Fatal("bridge port accepted")
	}
	if err := s.checkConsolePort(70000); err == nil {
		t.Fatal("out of range accepted")
	}
	if err := s.checkConsolePort(8080); err != nil {
		t.Fatal(err)
	}
	// 从局域网关闭局域网访问会被拒绝
	tok := s.sess.newToken()
	id, _ := s.sess.redeem(tok)
	hdr := map[string]string{"Cookie": sessionCookie + "=" + id}
	s.deps.Config.Access.LAN = true
	if w := do(s, "PUT", "/api/console", lanAddr, `{"lan":false}`, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("LAN disabling LAN: %d", w.Code)
	}
}
