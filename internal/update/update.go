// Package update 实现自更新：读取发布清单、比较版本、下载并校验新程序、替换可执行文件。
//
// 清单是一个 JSON 文件（发布流程生成的 manifest.json），按 GOOS-GOARCH 列出每个平台
// 的下载地址、大小和 SHA-256。下载完成后新程序放在数据目录的 update\ 下，并记录到
// pending.json；真正替换文件由 deploy 包的 update-apply 动作完成，需要时提权执行。
//
// 清单通过 HTTPS 获取，下载内容按清单中的 SHA-256 校验，因此信任边界是清单所在的站点。
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manifest 是发布清单。
type Manifest struct {
	Version     string           `json:"version"`
	Notes       string           `json:"notes,omitempty"`
	PublishedAt string           `json:"publishedAt,omitempty"`
	Assets      map[string]Asset `json:"assets"` // 键为 GOOS-GOARCH，如 windows-amd64
}

// Asset 是清单中一个平台的程序文件。
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Latest 是检查到的最新版本摘要。
type Latest struct {
	Version     string `json:"version"`
	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

// Pending 是已下载、等待应用的程序。
type Pending struct {
	Version      string    `json:"version"`
	File         string    `json:"file"`
	SHA256       string    `json:"sha256"`
	Size         int64     `json:"size"`
	DownloadedAt time.Time `json:"downloadedAt"`
}

// Status 是对界面暴露的更新状态。
type Status struct {
	Current     string     `json:"current"`
	Platform    string     `json:"platform"`
	URL         string     `json:"url"`
	AutoCheck   bool       `json:"autoCheck"`
	Checking    bool       `json:"checking"`
	LastCheck   *time.Time `json:"lastCheck,omitempty"`
	Available   bool       `json:"available"` // 最新版本比当前新
	Latest      *Latest    `json:"latest,omitempty"`
	Downloading bool       `json:"downloading"`
	Received    int64      `json:"received"`
	Total       int64      `json:"total"`
	Downloaded  *Pending   `json:"downloaded,omitempty"` // 已下载、可应用的版本
	Error       string     `json:"error,omitempty"`
}

// Options 是 Updater 的参数。
type Options struct {
	DataDir   string
	Current   string // 当前程序版本
	URL       string // 清单地址
	AutoCheck bool
	Log       *slog.Logger
	OnChange  func() // 状态变化时回调，用于推送界面
}

// Updater 管理检查与下载。替换文件见 ApplyPending。
type Updater struct {
	dir    string
	log    *slog.Logger
	client *http.Client
	notify func()

	mu  sync.Mutex
	st  Status
	dl  context.CancelFunc
	err error
}

// Platform 返回当前平台键。
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// Dir 返回数据目录下的更新目录。
func Dir(dataDir string) string { return filepath.Join(dataDir, "update") }

// New 创建 Updater。
func New(o Options) *Updater {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	u := &Updater{
		dir:    Dir(o.DataDir),
		log:    o.Log,
		client: &http.Client{Timeout: 0, Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second, Proxy: http.ProxyFromEnvironment}},
		notify: o.OnChange,
		st: Status{
			Current:   o.Current,
			Platform:  Platform(),
			URL:       o.URL,
			AutoCheck: o.AutoCheck,
		},
	}
	if p, ok := ReadPending(o.DataDir); ok {
		u.st.Downloaded = &p
	}
	return u
}

// Status 返回状态副本。
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.st
	if s.Latest != nil {
		l := *s.Latest
		s.Latest = &l
	}
	if s.Downloaded != nil {
		d := *s.Downloaded
		s.Downloaded = &d
	}
	return s
}

// SetConfig 更新清单地址与自动检查开关。
func (u *Updater) SetConfig(url string, auto bool) {
	u.mu.Lock()
	if url != u.st.URL {
		u.st.Latest = nil
		u.st.Available = false
		u.st.LastCheck = nil
	}
	u.st.URL = url
	u.st.AutoCheck = auto
	u.mu.Unlock()
	u.emit()
}

func (u *Updater) emit() {
	if u.notify != nil {
		u.notify()
	}
}

func (u *Updater) setError(err error) {
	u.mu.Lock()
	if err != nil {
		u.st.Error = err.Error()
	} else {
		u.st.Error = ""
	}
	u.mu.Unlock()
}

// Check 读取清单并比较版本。
func (u *Updater) Check(ctx context.Context) (Status, error) {
	u.mu.Lock()
	if u.st.Checking {
		u.mu.Unlock()
		return u.Status(), errors.New("正在检查")
	}
	url := u.st.URL
	u.st.Checking = true
	u.st.Error = ""
	u.mu.Unlock()
	u.emit()

	m, err := u.fetchManifest(ctx, url)
	now := time.Now()
	u.mu.Lock()
	u.st.Checking = false
	u.st.LastCheck = &now
	if err != nil {
		u.st.Error = err.Error()
		u.mu.Unlock()
		u.emit()
		return u.Status(), err
	}
	asset, ok := m.Assets[Platform()]
	if !ok {
		u.st.Latest = &Latest{Version: m.Version, Notes: m.Notes, PublishedAt: m.PublishedAt}
		u.st.Available = false
		u.st.Error = fmt.Sprintf("清单中没有 %s 的程序", Platform())
		u.mu.Unlock()
		u.emit()
		return u.Status(), errors.New(u.st.Error)
	}
	u.st.Latest = &Latest{Version: m.Version, Notes: m.Notes, PublishedAt: m.PublishedAt, URL: asset.URL, SHA256: strings.ToLower(asset.SHA256), Size: asset.Size}
	u.st.Available = Newer(u.st.Current, m.Version)
	u.mu.Unlock()
	u.emit()
	return u.Status(), nil
}

func (u *Updater) fetchManifest(ctx context.Context, url string) (*Manifest, error) {
	if url == "" {
		return nil, errors.New("未配置更新地址")
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "rsagent/"+u.st.Current)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取清单: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取清单: HTTP %d", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("清单格式错误: %w", err)
	}
	if m.Version == "" {
		return nil, errors.New("清单缺少 version")
	}
	return &m, nil
}

// StartDownload 在后台下载最新版本，立即返回。进度通过 Status 查看。
func (u *Updater) StartDownload(ctx context.Context) error {
	u.mu.Lock()
	if u.st.Downloading {
		u.mu.Unlock()
		return errors.New("正在下载")
	}
	if u.st.Latest == nil || u.st.Latest.URL == "" {
		u.mu.Unlock()
		return errors.New("请先检查更新")
	}
	if !u.st.Available {
		u.mu.Unlock()
		return errors.New("当前已是最新版本")
	}
	if u.st.Downloaded != nil && u.st.Downloaded.Version == u.st.Latest.Version {
		if err := VerifyFile(u.st.Downloaded.File, u.st.Downloaded.SHA256); err == nil {
			u.mu.Unlock()
			return errors.New("该版本已下载，可直接应用")
		}
	}
	latest := *u.st.Latest
	dctx, cancel := context.WithCancel(ctx)
	u.dl = cancel
	u.st.Downloading = true
	u.st.Received, u.st.Total = 0, latest.Size
	u.st.Error = ""
	u.mu.Unlock()
	u.emit()

	go func() {
		p, err := u.download(dctx, latest)
		u.mu.Lock()
		u.st.Downloading = false
		u.dl = nil
		if err != nil {
			u.st.Error = err.Error()
			u.log.Warn("下载更新失败", "version", latest.Version, "err", err)
		} else {
			u.st.Downloaded = &p
			u.log.Info("更新已下载", "version", p.Version, "file", p.File, "size", p.Size)
		}
		u.mu.Unlock()
		u.emit()
	}()
	return nil
}

// CancelDownload 取消进行中的下载。
func (u *Updater) CancelDownload() {
	u.mu.Lock()
	cancel := u.dl
	u.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (u *Updater) download(ctx context.Context, l Latest) (Pending, error) {
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return Pending{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return Pending{}, err
	}
	req.Header.Set("User-Agent", "rsagent/"+u.st.Current)
	resp, err := u.client.Do(req)
	if err != nil {
		return Pending{}, fmt.Errorf("下载: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Pending{}, fmt.Errorf("下载: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 {
		u.mu.Lock()
		u.st.Total = resp.ContentLength
		u.mu.Unlock()
	}

	tmp := filepath.Join(u.dir, "download.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return Pending{}, err
	}
	h := sha256.New()
	buf := make([]byte, 256<<10)
	var n int64
	lastEmit := time.Now()
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, werr := f.Write(buf[:k]); werr != nil {
				f.Close()
				os.Remove(tmp)
				return Pending{}, werr
			}
			h.Write(buf[:k])
			n += int64(k)
			if time.Since(lastEmit) > 500*time.Millisecond {
				u.mu.Lock()
				u.st.Received = n
				u.mu.Unlock()
				u.emit()
				lastEmit = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			if ctx.Err() != nil {
				return Pending{}, errors.New("下载已取消")
			}
			return Pending{}, fmt.Errorf("下载中断: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Pending{}, err
	}
	u.mu.Lock()
	u.st.Received = n
	u.mu.Unlock()

	sum := hex.EncodeToString(h.Sum(nil))
	if l.SHA256 != "" && sum != l.SHA256 {
		os.Remove(tmp)
		return Pending{}, fmt.Errorf("SHA-256 校验失败：清单 %s，实际 %s", l.SHA256, sum)
	}
	if l.Size > 0 && n != l.Size {
		os.Remove(tmp)
		return Pending{}, fmt.Errorf("大小不符：清单 %d，实际 %d", l.Size, n)
	}
	dst := filepath.Join(u.dir, "rsagent-"+sanitize(l.Version)+exeSuffix())
	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return Pending{}, err
	}
	p := Pending{Version: l.Version, File: dst, SHA256: sum, Size: n, DownloadedAt: time.Now()}
	if err := WritePending(filepath.Dir(u.dir), p); err != nil {
		return Pending{}, err
	}
	// 清理旧版本的下载文件
	entries, _ := os.ReadDir(u.dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "rsagent-") && filepath.Join(u.dir, name) != dst {
			_ = os.Remove(filepath.Join(u.dir, name))
		}
	}
	return p, nil
}

// ClearPending 删除已下载的程序与记录。
func (u *Updater) ClearPending() {
	u.mu.Lock()
	p := u.st.Downloaded
	u.st.Downloaded = nil
	u.mu.Unlock()
	if p != nil {
		_ = os.Remove(p.File)
	}
	_ = os.Remove(pendingPath(filepath.Dir(u.dir)))
	u.emit()
}

// AutoCheckLoop 按配置定期检查，发现新版本只记录状态，不自动下载。
func (u *Updater) AutoCheckLoop(ctx context.Context) {
	first := time.NewTimer(2 * time.Minute)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	for {
		if u.Status().AutoCheck && Parse(u.Status().Current).valid {
			if st, err := u.Check(ctx); err == nil && st.Available {
				u.log.Info("发现新版本", "current", st.Current, "latest", st.Latest.Version)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// ---- 待应用记录 ----

func pendingPath(dataDir string) string { return filepath.Join(Dir(dataDir), "pending.json") }

// WritePending 记录已下载的程序。
func WritePending(dataDir string, p Pending) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pendingPath(dataDir), data, 0o644)
}

// ReadPending 读取待应用记录，文件缺失或校验失败返回 false。
func ReadPending(dataDir string) (Pending, bool) {
	var p Pending
	data, err := os.ReadFile(pendingPath(dataDir))
	if err != nil {
		return p, false
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, false
	}
	if err := VerifyFile(p.File, p.SHA256); err != nil {
		return p, false
	}
	return p, true
}

// RemovePending 删除记录与文件。
func RemovePending(dataDir string) {
	if p, ok := ReadPending(dataDir); ok {
		_ = os.Remove(p.File)
	}
	_ = os.Remove(pendingPath(dataDir))
}

// CleanupApplied 在程序启动时调用：已下载的版本就是当前运行的版本，说明更新已应用，删除下载文件。
// 只由拥有程序文件的进程（ui、run）调用；服务模式下保留给托盘程序自更新用。
func CleanupApplied(dataDir, current string) {
	if p, ok := ReadPending(dataDir); ok && p.Version == current {
		RemovePending(dataDir)
	}
}

// VerifyFile 校验文件的 SHA-256。
func VerifyFile(path, sum string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, sum) {
		return fmt.Errorf("SHA-256 不符：期望 %s，实际 %s", sum, got)
	}
	return nil
}

// FileSHA256 计算文件 SHA-256。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReplaceExecutable 用 src 替换 target。运行中的程序文件不能覆盖但可以改名，
// 所以先把 target 改名为 .old，再把 src 复制过去；失败时改回。
func ReplaceExecutable(target, src string) error {
	old := target + ".old"
	_ = os.Remove(old)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("改名旧程序: %w", err)
		}
	}
	if err := copyFile(src, target); err != nil {
		_ = os.Remove(target)
		if _, serr := os.Stat(old); serr == nil {
			_ = os.Rename(old, target)
		}
		return fmt.Errorf("写入新程序: %w", err)
	}
	return nil
}

// CleanupOld 删除替换时留下的 .old 文件，启动时调用；仍被占用则忽略。
func CleanupOld(exe string) {
	_ = os.Remove(exe + ".old")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ---- 版本比较 ----

// Version 是解析后的版本号。
type Version struct {
	nums  [3]int
	pre   string
	valid bool
}

// Parse 解析 "v1.2.3"、"1.2.3-beta1" 这类版本号；无法解析（如 dev）时 valid 为 false。
func Parse(s string) Version {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V"))
	if s == "" {
		return Version{}
	}
	var v Version
	main := s
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		main = s[:i]
		if s[i] == '-' {
			v.pre = s[i+1:]
			if j := strings.Index(v.pre, "+"); j >= 0 {
				v.pre = v.pre[:j]
			}
		}
	}
	parts := strings.Split(main, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}
		}
		v.nums[i] = n
	}
	v.valid = true
	return v
}

// Compare 比较两个版本：-1、0、1。无法解析的版本视为最旧。
func Compare(a, b string) int {
	va, vb := Parse(a), Parse(b)
	switch {
	case !va.valid && !vb.valid:
		return strings.Compare(a, b)
	case !va.valid:
		return -1
	case !vb.valid:
		return 1
	}
	for i := 0; i < 3; i++ {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1
			}
			return 1
		}
	}
	// 有预发布后缀的比正式版旧
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	}
	return comparePre(va.pre, vb.pre)
}

func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aerr == nil && berr == nil:
			c = compareInt(an, bn)
		case aerr == nil:
			c = -1
		case berr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return compareInt(len(as), len(bs))
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Newer 判断 latest 是否比 current 新。current 无法解析（dev 构建）时总是返回 true。
func Newer(current, latest string) bool {
	return Compare(current, latest) < 0
}
