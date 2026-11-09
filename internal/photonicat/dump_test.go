package photonicat

import (
	"encoding/json"
	"os"
	"testing"
)

// RSAGENT_SCREEN_DUMP=dir go test ./internal/photonicat -run TestDumpPage 输出页面 JSON，供预览工具绘制。
func TestDumpPage(t *testing.T) {
	dir := os.Getenv("RSAGENT_SCREEN_DUMP")
	if dir == "" {
		t.Skip("set RSAGENT_SCREEN_DUMP to dump the page layout")
	}
	b, _ := json.MarshalIndent(Page(), "", " ")
	if err := os.WriteFile(dir+"/page.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
}
