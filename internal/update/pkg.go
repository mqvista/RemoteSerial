package update

// OpenWrt 上由 opkg / apk 安装时的更新：下载新版本的 .ipk / .apk，交给套件管理器安装。
// 程序文件归套件管理器所有，不能像其他平台那样直接替换。
//
// 安装时套件的安装后脚本会重启 rsagent，所以安装放在一个独立会话的 shell 脚本里执行，
// 不随 rsagent 退出；结果写到 pkg-apply.log，新进程启动时据此清理或报告失败。

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"rsagent/internal/i18n"
	"rsagent/internal/platform"
	"runtime"
	"strings"
	"time"
)

// PkgFile 是清单中的一个套件文件。
type PkgFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var pkgBase = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// pkgVersion 与 tools/owpkg 相同：0.6.0 → 0.6.0-r1；预发布版 opkg 用 ~preN，apk 用 _preN（N 为后缀里的数字）。
func pkgVersion(v, mgr string) (string, bool) {
	v = strings.TrimPrefix(v, "v")
	base, suffix, _ := strings.Cut(v, "-")
	if !pkgBase.MatchString(base) {
		return "", false
	}
	n := strings.Join(preDigits.FindAllString(suffix, -1), "")
	switch {
	case suffix == "":
		return base + "-r1", true
	case mgr == "apk":
		return base + "_pre" + n + "-r1", true
	default:
		return base + "~pre" + n + "-r1", true
	}
}

var preDigits = regexp.MustCompile(`[0-9]+`)

// pkgArch 是套件的架构名，与 tools/owpkg 的对应表一致。
func pkgArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64_generic"
	}
	return runtime.GOARCH
}

// packageNames 返回某版本需要的套件文件名：rsagent，以及已安装时的 luci-app-rsagent。
func packageNames(mgr, version string, withLuci bool) []string {
	pv, ok := pkgVersion(version, mgr)
	if !ok {
		return nil
	}
	var names []string
	if mgr == "apk" {
		names = append(names, fmt.Sprintf("rsagent-%s_%s.apk", pv, pkgArch()))
		if withLuci {
			names = append(names, fmt.Sprintf("luci-app-rsagent-%s_all.apk", pv))
		}
	} else {
		names = append(names, fmt.Sprintf("rsagent_%s_%s.ipk", pv, pkgArch()))
		if withLuci {
			names = append(names, fmt.Sprintf("luci-app-rsagent_%s_all.ipk", pv))
		}
	}
	return names
}

// extraPackage 返回不分架构的附加套件的文件名。
func extraPackage(mgr, version, name string) []string {
	pv, ok := pkgVersion(version, mgr)
	if !ok {
		return nil
	}
	if mgr == "apk" {
		return []string{fmt.Sprintf("%s-%s_all.apk", name, pv)}
	}
	return []string{fmt.Sprintf("%s_%s_all.ipk", name, pv)}
}

// latestPackages 从清单里找出本机需要的套件：rsagent，以及已安装的 luci-app-rsagent、rsagent-photonicat。
func latestPackages(m *Manifest, mgr string) ([]PkgFile, error) {
	names := packageNames(mgr, m.Version, platform.PackageInstalled("luci-app-rsagent") != "")
	if platform.PackageInstalled("rsagent-photonicat") != "" {
		names = append(names, extraPackage(mgr, m.Version, "rsagent-photonicat")...)
	}
	if len(names) == 0 {
		return nil, i18n.Errorf("版本号 %q 无法对应套件版本", m.Version)
	}
	out := make([]PkgFile, 0, len(names))
	for _, n := range names {
		a, ok := m.Files[n]
		if !ok || a.URL == "" {
			return nil, i18n.Errorf("清单中没有套件 %s", n)
		}
		out = append(out, PkgFile{Name: n, URL: a.URL, SHA256: strings.ToLower(a.SHA256), Size: a.Size})
	}
	return out, nil
}

// downloadPackages 依次下载套件并校验。进度按总大小累计。
func (u *Updater) downloadPackages(ctx context.Context, l Latest) (Pending, error) {
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return Pending{}, err
	}
	var total int64
	for _, f := range l.Packages {
		total += f.Size
	}
	u.mu.Lock()
	u.st.Total = total
	u.mu.Unlock()

	var done int64
	paths := make([]string, 0, len(l.Packages))
	for _, f := range l.Packages {
		dst := filepath.Join(u.dir, f.Name)
		n, err := u.fetchTo(ctx, f, dst, done)
		if err != nil {
			for _, p := range paths {
				_ = os.Remove(p)
			}
			return Pending{}, err
		}
		done += n
		paths = append(paths, dst)
	}
	p := Pending{Version: l.Version, File: paths[0], SHA256: l.Packages[0].SHA256, Size: done, DownloadedAt: time.Now(), Packages: paths}
	if err := WritePending(filepath.Dir(u.dir), p); err != nil {
		return Pending{}, err
	}
	// 清理旧版本的套件与程序
	keep := map[string]bool{}
	for _, p := range paths {
		keep[p] = true
	}
	entries, _ := os.ReadDir(u.dir)
	for _, e := range entries {
		full := filepath.Join(u.dir, e.Name())
		if (strings.HasSuffix(e.Name(), ".ipk") || strings.HasSuffix(e.Name(), ".apk") || strings.HasPrefix(e.Name(), "rsagent-")) && !keep[full] {
			_ = os.Remove(full)
		}
	}
	return p, nil
}

func (u *Updater) fetchTo(ctx context.Context, f PkgFile, dst string, base int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "rsagent/"+u.st.Current)
	resp, err := u.client.Do(req)
	if err != nil {
		return 0, i18n.Errorf("下载: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, i18n.Errorf("下载 %s: HTTP %d", f.Name, resp.StatusCode)
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	pr := &progressReader{r: resp.Body, onRead: func(n int64) {
		u.mu.Lock()
		u.st.Received = base + n
		u.mu.Unlock()
	}, emit: u.emit}
	n, err := io.Copy(io.MultiWriter(out, h), pr)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return 0, i18n.New("下载已取消")
		}
		return 0, i18n.Errorf("下载中断: %w", err)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); f.SHA256 != "" && sum != f.SHA256 {
		_ = os.Remove(tmp)
		return 0, i18n.Errorf("SHA-256 校验失败：清单 %s，实际 %s", f.SHA256, sum)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

type progressReader struct {
	r      io.Reader
	n      int64
	last   time.Time
	onRead func(int64)
	emit   func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	k, err := p.r.Read(b)
	p.n += int64(k)
	p.onRead(p.n)
	if time.Since(p.last) > 500*time.Millisecond {
		p.last = time.Now()
		p.emit()
	}
	return k, err
}

func applyLogPath(dir string) string { return filepath.Join(dir, "pkg-apply.log") }

// ApplyPackages 在独立会话里用套件管理器安装已下载的套件，立即返回。
// 安装成功时 rsagent 会被套件的安装后脚本重启；失败时本进程仍在运行，从日志读出原因。
func (u *Updater) ApplyPackages() error {
	u.mu.Lock()
	p, mgr := u.st.Downloaded, u.st.PackageManager
	if u.st.Installing {
		u.mu.Unlock()
		return i18n.New("正在安装，请稍候")
	}
	u.mu.Unlock()
	if mgr == "" {
		return i18n.New("rsagent 不是用套件安装的")
	}
	if p == nil || len(p.Packages) == 0 {
		return i18n.New("没有已下载的更新，请先下载")
	}
	for _, f := range p.Packages {
		if _, err := os.Stat(f); err != nil {
			return i18n.Errorf("已下载的套件不见了，请重新下载: %w", err)
		}
	}
	files := ""
	for _, f := range p.Packages {
		files += " " + shellQuote(f)
	}
	var install string
	if mgr == "apk" {
		install = "apk update >/dev/null 2>&1; apk add --allow-untrusted" + files
	} else {
		install = "opkg update >/dev/null 2>&1; opkg install" + files
	}
	logFile := applyLogPath(u.dir)
	script := filepath.Join(u.dir, "pkg-apply.sh")
	body := "#!/bin/sh\n" +
		"# Generated by rsagent: install the downloaded update with " + mgr + ".\n" +
		"# Runs in its own session because installing restarts rsagent.\n" +
		"exec >" + shellQuote(logFile) + " 2>&1\n" +
		"sleep 1\n" +
		"echo \"version=" + p.Version + "\"\n" +
		install + "\n" +
		"echo \"exit=$?\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return err
	}
	_ = os.Remove(logFile)
	if err := SpawnDetached("/bin/sh", []string{script}, nil); err != nil {
		return i18n.Errorf("启动安装失败: %w", err)
	}
	u.mu.Lock()
	u.st.Installing = true
	u.errTxt = nil
	u.mu.Unlock()
	u.emit()
	u.log.Info(i18n.T("开始用套件管理器安装更新"), "version", p.Version, "manager", mgr)
	go u.watchApply(p.Version)
	return nil
}

// watchApply 等安装脚本结束。成功时本进程会被重启，通常等不到；失败时报告日志末尾。
func (u *Updater) watchApply(version string) {
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		code, tail, ok := readApplyLog(u.dir)
		if !ok {
			continue
		}
		u.mu.Lock()
		u.st.Installing = false
		if code != 0 {
			u.errTxt = i18n.ErrText(i18n.Errorf("套件安装失败（退出码 %d）：%s", code, tail))
		}
		u.mu.Unlock()
		u.emit()
		if code != 0 {
			u.log.Warn(i18n.T("套件安装失败"), "version", version, "exit", code, "log", tail)
		}
		return
	}
	u.mu.Lock()
	u.st.Installing = false
	u.errTxt = i18n.ErrText(i18n.New("套件安装超时，请查看 /tmp/rsagent/update/pkg-apply.log"))
	u.mu.Unlock()
	u.emit()
}

// readApplyLog 读出安装脚本的退出码与日志末尾几行；脚本未结束时 ok 为 false。
func readApplyLog(dir string) (code int, tail string, ok bool) {
	f, err := os.Open(applyLogPath(dir))
	if err != nil {
		return 0, "", false
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, found := strings.CutPrefix(line, "exit="); found {
			_, _ = fmt.Sscan(v, &code)
			ok = true
			continue
		}
		if line != "" && !strings.HasPrefix(line, "version=") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return code, strings.Join(lines, " / "), ok
}

// checkPackageApplied 在启动时调用：已下载的版本就是当前版本，说明套件已装好，清理下载；
// 版本没变而安装日志显示失败，则报告失败原因。
func (u *Updater) checkPackageApplied(dataDir string) {
	p, ok := ReadPending(dataDir)
	if !ok {
		return
	}
	if p.Version == u.st.Current {
		RemovePending(dataDir)
		u.st.Downloaded = nil
		u.log.Info(i18n.T("套件更新已完成"), "version", p.Version)
		return
	}
	if code, tail, done := readApplyLog(u.dir); done && code != 0 {
		u.errTxt = i18n.ErrText(i18n.Errorf("套件安装失败（退出码 %d）：%s", code, tail))
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
