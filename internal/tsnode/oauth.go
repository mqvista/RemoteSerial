package tsnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2/clientcredentials"

	"rsagent/internal/i18n"
)

// resolveOAuthKey 把 OAuth client secret（tskey-client-…[?ephemeral=…&preauthorized=…&baseURL=…]）
// 换成一次性 auth key，其他 key 原样返回。
//
// 内嵌节点由 tsnet 自己完成这一步；系统 tailscaled 不会替调用方做，tailscale up 是在 CLI 里换的，
// 所以 system 后端在这里按同样的规则换好再交给 tailscaled。
func resolveOAuthKey(ctx context.Context, key string, tags []string) (string, error) {
	if !strings.HasPrefix(key, "tskey-client-") {
		return key, nil
	}
	if len(tags) == 0 {
		return "", i18n.New("使用 OAuth client secret 登录必须指定 tag，例如 tag:serial-agent")
	}
	secret, named, _ := strings.Cut(key, "?")
	attrs, err := url.ParseQuery(named)
	if err != nil {
		return "", err
	}
	getBool := func(name string, def bool) (bool, error) {
		v := attrs.Get(name)
		if v == "" {
			return def, nil
		}
		return strconv.ParseBool(v)
	}
	// 与 tsnet 登录时一致：默认建持久、预先批准的节点。
	ephemeral, err := getBool("ephemeral", false)
	if err != nil {
		return "", err
	}
	preauth, err := getBool("preauthorized", true)
	if err != nil {
		return "", err
	}
	base := "https://api.tailscale.com"
	if v := attrs.Get("baseURL"); v != "" {
		base = strings.TrimSuffix(v, "/")
	}
	cc := clientcredentials.Config{ClientID: "rsagent", ClientSecret: secret, TokenURL: base + "/api/v2/oauth/token"}
	body, _ := json.Marshal(map[string]any{
		"capabilities": map[string]any{
			"devices": map[string]any{
				"create": map[string]any{"reusable": false, "ephemeral": ephemeral, "preauthorized": preauth, "tags": tags},
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/tailnet/-/keys", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cc.Client(ctx).Do(req)
	if err != nil {
		return "", i18n.Errorf("用 OAuth client secret 换取 auth key 失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", i18n.Errorf("用 OAuth client secret 换取 auth key 失败: %w", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data))))
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Key == "" {
		return "", i18n.New("Tailscale API 没有返回 auth key")
	}
	return out.Key, nil
}
