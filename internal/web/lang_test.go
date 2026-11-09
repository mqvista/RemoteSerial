package web

import (
	"net/http"
	"testing"

	"rsagent/internal/i18n"
)

func TestLanguageFollow(t *testing.T) {
	defer i18n.SetLang(i18n.Current())
	s := testServer(t)
	i18n.SetLang(i18n.EN)
	if w := do(s, "POST", "/api/language/follow", "127.0.0.1:1", `{"lang":"zh-tw"}`, nil); w.Code != http.StatusOK || i18n.Current() != i18n.TW {
		t.Fatalf("follow: %d %s, lang %s", w.Code, w.Body, i18n.Current())
	}
	// 在管理界面选过语言时不跟随
	s.deps.Config.Language = "en"
	i18n.SetLang(i18n.EN)
	if do(s, "POST", "/api/language/follow", "127.0.0.1:1", `{"lang":"zh-tw"}`, nil); i18n.Current() != i18n.EN {
		t.Fatal("followed although a language is configured")
	}
	s.deps.Config.Language = ""
	if w := do(s, "POST", "/api/language/follow", lanAddr, `{"lang":"zh-tw"}`, nil); w.Code == http.StatusOK {
		t.Fatal("accepted from the LAN")
	}
}
