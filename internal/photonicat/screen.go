// Package photonicat 在 Photonicat 2 机身的小屏幕上加一页 RemoteSerial 状态。
//
// 屏幕由原厂的 pcat2-display-mini 驱动（https://github.com/photonicat/photonicat2_mini_display）：
// 用户配置 /etc/pcat2_mini_display-user_config.json 深度合并到内置默认配置之上，同名页面整页覆盖；
// 页数就是页面个数；默认的最后一页是 GPS 页（含 Gps* 数据键，没有 GPS 时隐藏），所以本页插在它之前。
// 数据用 json_file 来源读取 rsagent 写的状态文件，一个来源对应全部数据键。
package photonicat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"rsagent/internal/i18n"
)

const (
	// InitScript 是原厂屏幕程序的服务脚本。
	InitScript = "/etc/init.d/pcat2-display-mini"
	// StatusFile 是 rsagent 写、屏幕读的状态文件（内存中，不磨损闪存）。
	StatusFile = "/tmp/rsagent/screen.json"
	// SourceName 是屏幕配置里本程序的数据来源名称。
	SourceName = "RemoteSerial"
	// markerKey 用来识别本页：只有本页用这个数据键。屏幕程序回写配置时会丢掉它不认识的字段，
	// fixed_text 又直接显示 label，所以不能另加标记字段。
	markerKey = "RSTsState"
	apiBase   = "http://127.0.0.1:8081/api/v1/"
)

// 测试时替换。
var (
	UserConfig = "/etc/pcat2_mini_display-user_config.json"
	BackupFile = "/etc/rsagent/pcat2_mini_display-user_config.backup.json"
	fetch      = fetchAPI
	restart    = func() error { return exec.Command(InitScript, "restart").Run() }
)

// Available 判断这台设备有没有 Photonicat 的屏幕程序。
func Available() bool { return availableFn() }

var availableFn = func() bool {
	_, err := os.Stat(InitScript)
	return err == nil
}

// Info 是屏幕集成的状态。
type Info struct {
	Display   bool `json:"display"`   // 有屏幕程序
	Installed bool `json:"installed"` // 屏幕配置里有本程序的数据来源
}

// Status 读取屏幕集成的状态。
func Status() Info {
	info := Info{Display: Available()}
	if user, err := readUser(); err == nil {
		info.Installed = hasSource(user)
	}
	return info
}

func fetchAPI(path string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, i18n.Errorf("无法连接屏幕程序（%s 是否在运行？）: %w", InitScript, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, i18n.Errorf("屏幕程序返回 HTTP %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func readUser() (map[string]any, error) {
	data, err := os.ReadFile(UserConfig)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, i18n.Errorf("屏幕用户配置不是有效的 JSON: %w", err)
	}
	return m, nil
}

func writeUser(m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := UserConfig + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, UserConfig)
}

// backup 在第一次修改前保存原来的用户配置。
func backup() {
	if _, err := os.Stat(BackupFile); err == nil {
		return
	}
	data, err := os.ReadFile(UserConfig)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(BackupFile), 0o700)
	_ = os.WriteFile(BackupFile, data, 0o600)
}

// pages 取出 display_template.elements（pageN → 元素列表）。
func pages(cfg map[string]any) map[string]any {
	dt, _ := cfg["display_template"].(map[string]any)
	if dt == nil {
		return map[string]any{}
	}
	el, _ := dt["elements"].(map[string]any)
	if el == nil {
		return map[string]any{}
	}
	return el
}

// userPages 返回（必要时建立）用户配置里的 elements。
func userPages(user map[string]any) map[string]any {
	dt, _ := user["display_template"].(map[string]any)
	if dt == nil {
		dt = map[string]any{}
		user["display_template"] = dt
	}
	el, _ := dt["elements"].(map[string]any)
	if el == nil {
		el = map[string]any{}
		dt["elements"] = el
	}
	return el
}

func pageKey(i int) string { return "page" + strconv.Itoa(i) }

func pageCount(el map[string]any) int {
	n := 0
	for {
		if _, ok := el[pageKey(n)]; !ok {
			return n
		}
		n++
	}
}

func elementsOf(v any) []any {
	list, _ := v.([]any)
	return list
}

func isOurPage(v any) bool {
	for _, e := range elementsOf(v) {
		if m, ok := e.(map[string]any); ok && m["data_key"] == markerKey {
			return true
		}
	}
	return false
}

// isGpsPage 与屏幕程序的判断相同：含 Gps 开头的数据键。
func isGpsPage(v any) bool {
	for _, e := range elementsOf(v) {
		if m, ok := e.(map[string]any); ok {
			if k, _ := m["data_key"].(string); strings.HasPrefix(k, "Gps") {
				return true
			}
		}
	}
	return false
}

func findOurPage(el map[string]any) int {
	for i := 0; i < pageCount(el); i++ {
		if isOurPage(el[pageKey(i)]) {
			return i
		}
	}
	return -1
}

// prune 删除与默认页面完全相同的用户页面，以后固件更新默认页面时才能生效。
func prune(userEl, defEl map[string]any) {
	for k, v := range userEl {
		if d, ok := defEl[k]; ok && reflect.DeepEqual(normalize(v), normalize(d)) {
			delete(userEl, k)
		}
	}
}

// normalize 经过一次 JSON 往返，让数字类型等表示方式一致再比较。
func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func sources(m map[string]any) []any {
	cm, _ := m["custom_metrics"].(map[string]any)
	if cm == nil {
		return nil
	}
	list, _ := cm["sources"].([]any)
	return list
}

func hasSource(m map[string]any) bool {
	for _, s := range sources(m) {
		if sm, ok := s.(map[string]any); ok && sm["name"] == SourceName {
			return true
		}
	}
	return false
}

// setSources 写回来源列表。用户配置里没有来源列表时以合并后的列表为准（数组整体覆盖默认值）。
func setSources(user, merged map[string]any, keep func(map[string]any) bool, add map[string]any) {
	list := sources(user)
	if _, ok := user["custom_metrics"]; !ok {
		list = sources(merged)
	}
	out := []any{}
	for _, s := range list {
		if sm, ok := s.(map[string]any); ok && !keep(sm) {
			continue
		}
		out = append(out, s)
	}
	if add != nil {
		out = append(out, add)
	}
	cm, _ := user["custom_metrics"].(map[string]any)
	if cm == nil {
		cm = map[string]any{}
		if mcm, ok := merged["custom_metrics"].(map[string]any); ok {
			for k, v := range mcm {
				if k != "sources" {
					cm[k] = v
				}
			}
		}
		user["custom_metrics"] = cm
	}
	cm["sources"] = out
}

func notOurs(s map[string]any) bool { return s["name"] != SourceName }

// Install 在屏幕上加入（或更新）RemoteSerial 页与数据来源，并重启屏幕程序。
func Install(out io.Writer) error {
	if !Available() {
		return i18n.Errorf("这台设备没有 Photonicat 屏幕程序（%s）", InitScript)
	}
	merged, err := fetch("go_get_config.json")
	if err != nil {
		return err
	}
	def, err := fetch("go_get_default_config.json")
	if err != nil {
		return err
	}
	user, err := readUser()
	if err != nil {
		return err
	}
	backup()

	mEl, uEl := pages(merged), userPages(user)
	n := pageCount(mEl)
	if idx := findOurPage(mEl); idx >= 0 {
		fmt.Fprintf(out, "==> %s\n", i18n.Sprintf("更新屏幕第 %d 页（RemoteSerial）", idx+1))
		uEl[pageKey(idx)] = Page()
	} else {
		at := n
		if n > 0 && isGpsPage(mEl[pageKey(n-1)]) {
			at = n - 1 // 插在 GPS 页之前，GPS 页仍是最后一页
		}
		for i := n - 1; i >= at; i-- {
			uEl[pageKey(i+1)] = mEl[pageKey(i)]
		}
		uEl[pageKey(at)] = Page()
		fmt.Fprintf(out, "==> %s\n", i18n.Sprintf("在屏幕第 %d 页加入 RemoteSerial", at+1))
	}
	prune(uEl, pages(def))
	setSources(user, merged, notOurs, Source())
	if err := writeUser(user); err != nil {
		return i18n.Errorf("写入屏幕配置失败: %w", err)
	}
	fmt.Fprintf(out, "==> %s\n", i18n.T("重启屏幕程序"))
	if err := restart(); err != nil {
		return i18n.Errorf("重启屏幕程序失败: %w", err)
	}
	return nil
}

// Remove 移除 RemoteSerial 页与数据来源，后面的页面前移，并重启屏幕程序。
func Remove(out io.Writer) error {
	if !Available() {
		return nil
	}
	merged, err := fetch("go_get_config.json")
	if err != nil {
		return err
	}
	def, err := fetch("go_get_default_config.json")
	if err != nil {
		return err
	}
	user, err := readUser()
	if err != nil {
		return err
	}
	mEl, uEl := pages(merged), userPages(user)
	changed := false
	if idx := findOurPage(mEl); idx >= 0 {
		n := pageCount(mEl)
		for i := idx; i < n-1; i++ {
			uEl[pageKey(i)] = mEl[pageKey(i+1)]
		}
		delete(uEl, pageKey(n-1))
		fmt.Fprintf(out, "==> %s\n", i18n.Sprintf("从屏幕移除第 %d 页（RemoteSerial）", idx+1))
		changed = true
	}
	prune(uEl, pages(def))
	if hasSource(user) {
		setSources(user, merged, notOurs, nil)
		changed = true
	}
	if !changed {
		return nil
	}
	if err := writeUser(user); err != nil {
		return i18n.Errorf("写入屏幕配置失败: %w", err)
	}
	fmt.Fprintf(out, "==> %s\n", i18n.T("重启屏幕程序"))
	if err := restart(); err != nil {
		return i18n.Errorf("重启屏幕程序失败: %w", err)
	}
	return nil
}

// Source 是读取状态文件的 json_file 数据来源。
func Source() map[string]any {
	maps := []any{}
	for _, k := range []string{"tsState", "tsIP", "tsName", "bridges", "client", "rxRate", "txRate", "console", "version",
		"lblState", "lblBridges", "lblClient", "lblRx", "lblTx", "lblConsole"} {
		maps = append(maps, map[string]any{"json_path": k, "data_key": dataKey(k)})
	}
	return map[string]any{
		"type":    "json_file",
		"name":    SourceName,
		"enabled": 1,
		"config": map[string]any{
			"path":     StatusFile,
			"interval": 2,
			"mappings": maps,
		},
	}
}

// dataKey 给状态字段加上 RS 前缀：tsIP → RSTsIP，不与原厂数据键冲突。
func dataKey(field string) string {
	return "RS" + strings.ToUpper(field[:1]) + field[1:]
}

var (
	labelColor = []any{255, 229, 0}
	valueColor = []any{255, 229, 255}
)

func fixed(label, font string, x, y int) map[string]any {
	return map[string]any{"type": "fixed_text", "label": label, "position": map[string]any{"x": x, "y": y}, "font": font, "color": labelColor, "enable": 1}
}

func value(field string, x, y int) map[string]any {
	return map[string]any{"type": "text", "label": field, "position": map[string]any{"x": x, "y": y}, "font": "unit", "color": valueColor, "data_key": dataKey(field), "units_font": "unit", "enable": 1}
}

// label 是一个标签：用 text 元素读状态文件里的标签字段（fixed_text 只用 Orbitron，显示不了中文）。
func label(field string, x, y int) map[string]any {
	el := value(field, x, y)
	el["color"] = labelColor
	return el
}

// Page 是 RemoteSerial 页。中间区域 172×266 像素（左右各留 8 / 7 像素），每行 23 像素，标签黄色、数值白色；
// 可能很长的数值（IP、节点名、客户端）独占一行，超出宽度时屏幕程序会滚动显示；短数值放在标签右边。
// 标签跟随 rsagent 的语言，数值起点按两种语言里较宽的标签留足（实测 Orbitron Medium 15："RX:" 28、
// "Bridges:" 68、"Console:" 71 像素；中文用 Noto Sans Mono CJK，每字约 15 像素，最宽的"管理埠"45 像素）。
// 速率最长为 "999.9 KB/s"（约 96 像素）。
func Page() []any {
	return []any{
		fixed("RemoteSerial", "reg", 10, 4),
		label("lblState", 10, 32), value("tsState", 48, 32),
		value("tsIP", 10, 55),
		value("tsName", 10, 78),
		label("lblBridges", 10, 106), value("bridges", 86, 106),
		label("lblClient", 10, 129),
		value("client", 10, 152),
		label("lblRx", 10, 180), value("rxRate", 48, 180),
		label("lblTx", 10, 203), value("txRate", 48, 203),
		label("lblConsole", 10, 231), value("console", 88, 231),
	}
}
