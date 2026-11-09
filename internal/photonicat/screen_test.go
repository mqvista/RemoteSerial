package photonicat

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// deepMerge 与屏幕程序相同：对象逐键合并，数组与其他值整体覆盖。
func deepMerge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if vm, ok := v.(map[string]any); ok {
			dm, _ := dst[k].(map[string]any)
			if dm == nil {
				dm = map[string]any{}
			}
			dst[k] = deepMerge(dm, vm)
		} else {
			dst[k] = v
		}
	}
	return dst
}

// fakeDaemon 模拟屏幕程序。testdata 是按原厂默认配置的结构写的精简版：5 页，最后一页是含 Gps 数据键的 GPS 页。
func fakeDaemon(t *testing.T) (restarts *int) {
	t.Helper()
	dir := t.TempDir()
	oldUser, oldBackup, oldFetch, oldRestart := UserConfig, BackupFile, fetch, restart
	UserConfig, BackupFile = filepath.Join(dir, "user.json"), filepath.Join(dir, "backup.json")
	t.Cleanup(func() { UserConfig, BackupFile, fetch, restart = oldUser, oldBackup, oldFetch, oldRestart })
	raw, err := os.ReadFile("testdata/default_config.json")
	if err != nil {
		t.Fatal(err)
	}
	fetch = func(path string) (map[string]any, error) {
		var def map[string]any
		_ = json.Unmarshal(raw, &def)
		if path == "go_get_default_config.json" {
			return def, nil
		}
		user, err := readUser()
		if err != nil {
			return nil, err
		}
		return deepMerge(def, normalize(user).(map[string]any)), nil
	}
	n := 0
	restart = func() error { n++; return nil }
	return &n
}

func merged(t *testing.T) map[string]any {
	m, err := fetch("go_get_config.json")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallRemove(t *testing.T) {
	restarts := fakeDaemon(t)
	// 用户原本改过第 1 页，也有自己的数据来源
	user := map[string]any{
		"display_template": map[string]any{"elements": map[string]any{"page0": []any{map[string]any{"type": "fixed_text", "label": "Mine"}}}},
		"custom_metrics":   map[string]any{"sources": []any{map[string]any{"name": "MySource", "type": "command"}}},
	}
	if err := writeUser(user); err != nil {
		t.Fatal(err)
	}
	if !testAvailable(t) {
		return
	}
	if err := Install(io.Discard); err != nil {
		t.Fatal(err)
	}
	el := pages(merged(t))
	if pageCount(el) != 6 {
		t.Fatalf("pages after install: %d", pageCount(el))
	}
	if !isOurPage(el["page4"]) || !isGpsPage(el["page5"]) {
		t.Fatal("RemoteSerial should be page 5 and GPS stay last")
	}
	if elementsOf(el["page0"])[0].(map[string]any)["label"] != "Mine" {
		t.Fatal("user's own page changed")
	}
	u, _ := readUser()
	if !hasSource(u) || len(sources(u)) != 2 {
		t.Fatalf("sources: %v", sources(u))
	}
	// 再装一次：原地更新，不会多出一页
	if err := Install(io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := pageCount(pages(merged(t))); n != 6 {
		t.Fatalf("pages after reinstall: %d", n)
	}
	if err := Remove(io.Discard); err != nil {
		t.Fatal(err)
	}
	el = pages(merged(t))
	if pageCount(el) != 5 || !isGpsPage(el["page4"]) || findOurPage(el) >= 0 {
		t.Fatalf("after remove: %d pages", pageCount(el))
	}
	u, _ = readUser()
	uEl := pages(u)
	if _, ok := uEl["page4"]; ok {
		t.Error("page4 should fall back to the default GPS page")
	}
	if _, ok := uEl["page0"]; !ok || hasSource(u) || len(sources(u)) != 1 {
		t.Errorf("user config not restored: %v", u)
	}
	if *restarts != 3 {
		t.Errorf("restarts: %d", *restarts)
	}
	if _, err := os.Stat(BackupFile); err != nil {
		t.Error("no backup of the original user config")
	}
}

// Available 看的是真实路径；测试机上没有就临时跳过安装（逻辑仍由下面的单元覆盖）。
func testAvailable(t *testing.T) bool {
	if Available() {
		return true
	}
	old := availableFn
	availableFn = func() bool { return true }
	t.Cleanup(func() { availableFn = old })
	return true
}

func TestDataKey(t *testing.T) {
	if dataKey("tsIP") != "RSTsIP" || dataKey("rxRate") != "RSRxRate" {
		t.Fatal(dataKey("tsIP"))
	}
	if !isOurPage(Page()) || isGpsPage(Page()) {
		t.Fatal("page markers")
	}
}
