//go:build darwin

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"rsagent/internal/i18n"
)

// macOS 发布的是整个 .app 的 zip：替换整个包才能保持包签名完整。
// 服务使用的程序副本、以及不在包里运行的裸程序，用包里的主程序替换。
const downloadSuffix = ".zip"

// BundleOf 返回 exe 所在的 .app 路径；不在包里时返回空串。
func BundleOf(exe string) string {
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// extractApp 把 zip 解到 parent 下的临时目录，返回临时目录和其中的 .app。
// 放在目标同一目录下，之后用 rename 原子替换，不会跨卷。
func extractApp(zip, parent string) (tmp, app string, err error) {
	tmp, err = os.MkdirTemp(parent, ".rsagent-update-")
	if err != nil {
		return "", "", err
	}
	if out, err := exec.Command("ditto", "-x", "-k", zip, tmp).CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return "", "", i18n.Errorf("解压更新包失败: %s", strings.TrimSpace(string(out)))
	}
	matches, _ := filepath.Glob(filepath.Join(tmp, "*.app"))
	if len(matches) == 0 {
		os.RemoveAll(tmp)
		return "", "", i18n.New("更新包里没有 .app")
	}
	return tmp, matches[0], nil
}

// pendingBinary 从已下载的 zip 里取出主程序，缓存到 update 目录。
func pendingBinary(dataDir string, p Pending) (string, error) {
	bin := binCachePath(dataDir, p.Version)
	if bi, err := os.Stat(bin); err == nil {
		if zi, err := os.Stat(p.File); err == nil && !bi.ModTime().Before(zi.ModTime()) {
			return bin, nil
		}
	}
	tmp, app, err := extractApp(p.File, Dir(dataDir))
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := copyFile(filepath.Join(app, "Contents", "MacOS", "rsagent"), bin); err != nil {
		return "", err
	}
	return bin, nil
}

// ReplaceBundle 用 zip 里的 .app 替换 bundle：旧包改名为 .old，新包改名到原位，失败时还原。
// 以 root 运行时新包沿用旧包的属主，之后普通用户仍能自行更新。
func ReplaceBundle(bundle, zip string) error {
	tmp, app, err := extractApp(zip, filepath.Dir(bundle))
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if os.Geteuid() == 0 {
		if fi, err := os.Stat(bundle); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				_ = filepath.Walk(app, func(p string, _ os.FileInfo, err error) error {
					if err == nil {
						_ = os.Lchown(p, int(st.Uid), int(st.Gid))
					}
					return nil
				})
			}
		}
	}
	old := bundle + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(bundle, old); err != nil {
		return i18n.Errorf("改名旧程序: %w", err)
	}
	if err := os.Rename(app, bundle); err != nil {
		_ = os.Rename(old, bundle)
		return i18n.Errorf("写入新程序: %w", err)
	}
	return nil
}

func cleanupOldBundle(exe string) {
	if b := BundleOf(exe); b != "" {
		_ = os.RemoveAll(b + ".old")
	}
}
