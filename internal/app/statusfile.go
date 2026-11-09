package app

// 状态文件：配置了 statusFile 时每 2 秒写一次精简状态，供外部显示读取（如 Photonicat 小屏幕的 json_file 数据源）。
// 值都是格式化好的短字符串，读的一方直接显示；先写临时文件再改名，读的一方不会读到半个文件。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/buildinfo"
	"rsagent/internal/config"
	"rsagent/internal/i18n"
	"rsagent/internal/tsnode"
)

// ScreenStatus 是状态文件的内容。
type ScreenStatus struct {
	TSState string `json:"tsState"`
	TSIP    string `json:"tsIP"`
	TSName  string `json:"tsName"`
	Bridges string `json:"bridges"`
	Client  string `json:"client"`
	RxRate  string `json:"rxRate"`
	TxRate  string `json:"txRate"`
	Console string `json:"console"`
	Version string `json:"version"`
	Updated string `json:"updated"`
	// 页面标签也放在这里：屏幕程序只对 text 元素自动改用中文字体（fixed_text 显示不了中文），
	// 而且 rsagent 换语言时标签会跟着变
	LblState   string `json:"lblState"`
	LblBridges string `json:"lblBridges"`
	LblClient  string `json:"lblClient"`
	LblRx      string `json:"lblRx"`
	LblTx      string `json:"lblTx"`
	LblConsole string `json:"lblConsole"`
}

// screenState 把节点状态压成屏幕上放得下的短词，跟随 rsagent 的语言（屏幕对中文自动改用 CJK 字体）。
func screenState(s tsnode.State) string {
	switch s {
	case tsnode.StateRunning:
		return i18n.T("在线")
	case tsnode.StateNeedsLogin, tsnode.StateNotConfigured:
		return i18n.T("需登录")
	case tsnode.StateNeedsMachineAuth:
		return i18n.T("待批准")
	case tsnode.StateStarting:
		return i18n.T("连接中")
	case tsnode.StateNotInstalled:
		return i18n.T("未安装 TS")
	case tsnode.StateError:
		return i18n.T("出错")
	}
	return i18n.T("离线")
}

// formatRate 换算成 B/s、KB/s、MB/s（1 KB = 1024 B）。到 1000 就换下一级单位，
// 最长的值是 "999.9 KB/s"，小屏幕上一行放得下。
func formatRate(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B/s", n)
	case float64(n)/1024 < 999.95:
		return fmt.Sprintf("%.1f KB/s", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB/s", float64(n)/1024/1024)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func buildScreenStatus(cfg *config.Config, snap tsnode.Snapshot, bridges []bridge.Status, now time.Time) ScreenStatus {
	enabled, connected := 0, 0
	var rx, tx int64
	client := ""
	for _, b := range bridges {
		if !b.Enabled {
			continue
		}
		enabled++
		if b.State == bridge.StateConnected {
			connected++
			if client == "" {
				client = b.Client
			}
		}
		rx += b.RateFromDevice
		tx += b.RateToDevice
	}
	name, _, _ := strings.Cut(snap.DNSName, ".")
	return ScreenStatus{
		TSState: screenState(snap.State),
		TSIP:    dash(snap.IP),
		TSName:  dash(name),
		Bridges: fmt.Sprintf("%d/%d", connected, enabled),
		Client:  dash(client),
		RxRate:  formatRate(rx),
		TxRate:  formatRate(tx),
		Console: strconv.Itoa(cfg.ConsolePort()), // 只放端口号：标签已写明，Orbitron 的冒号又贴着数字
		Version: buildinfo.Version,
		Updated: now.Format("15:04:05"),

		LblState:   i18n.T("状态："),
		LblBridges: i18n.T("桥接："),
		LblClient:  i18n.T("客户端："),
		LblRx:      i18n.T("接收："),
		LblTx:      i18n.T("发送："),
		LblConsole: i18n.T("管理端口："),
	}
}

func writeStatusFile(path string, st ScreenStatus) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// statusFileLoop 每 2 秒写一次状态文件；statusFile 改为空时删掉旧文件。退出时也删掉，免得显示过时的状态。
func statusFileLoop(ctx context.Context, cfg *config.Config, node *tsnode.Node, bridges *bridge.Manager, log func(msg string, args ...any)) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	last, lastErr := "", ""
	defer func() {
		if last != "" {
			_ = os.Remove(last)
		}
	}()
	for {
		path := cfg.StatusFile
		if last != "" && path != last {
			_ = os.Remove(last)
		}
		last = path
		if path != "" {
			err := writeStatusFile(path, buildScreenStatus(cfg, node.Snapshot(), bridges.Status(), time.Now()))
			if msg := fmt.Sprint(err); err != nil && msg != lastErr {
				log(i18n.T("写入状态文件失败"), "file", path, "err", err)
				lastErr = msg
			} else if err == nil {
				lastErr = ""
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
