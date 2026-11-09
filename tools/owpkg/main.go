// owpkg 打 OpenWrt 套件，不需要 OpenWrt SDK（程序是预编译的，不用在 SDK 里编译）：
//
//   - .ipk（OpenWrt 24.10，opkg）：与 ipkg-build 相同的 tar.gz（debian-binary、control.tar.gz、data.tar.gz），本工具直接生成；
//   - .apk（OpenWrt 25.x，apk-tools v3）：用 Alpine 3.23 容器里的 apk mkpkg 生成，与 OpenWrt 构建系统用的是同一个 apk-tools v3。
//
// 产物直接放在 dist/（与其他发布文件一起，SHA256SUMS 与 manifest 会包含它们）：
// rsagent 与 luci-app-rsagent 两个套件，各架构的 .ipk 与 .apk。
//
//	go run ./tools/owpkg -version 0.6.0 -luci build/luci [-no-apk]
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"rsagent/internal/deploy"
)

const (
	maintainer = "mqvista <mqvista@users.noreply.github.com>"
	homepage   = "https://github.com/mqvista/RemoteSerial"
	dataDir    = "/etc/rsagent"
)

// 程序是静态编译的，不依赖 libc；常见 USB 转串口驱动直接作为依赖，免得用户再找驱动。
var rsagentDepends = []string{
	"tailscale", "kmod-usb-serial", "kmod-usb-acm",
	"kmod-usb-serial-ch341", "kmod-usb-serial-cp210x", "kmod-usb-serial-ftdi", "kmod-usb-serial-pl2303",
}

// 同一个 arm64 程序可用于所有 aarch64 目标；先提供 RK3568 等 armv8 目标使用的 aarch64_generic。
var arches = map[string]string{"amd64": "x86_64", "arm64": "aarch64_generic"}

const rsagentPostinst = `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
mkdir -p /etc/rsagent && chmod 700 /etc/rsagent
# 确保系统 tailscale 已启用并运行，tailscale0 属于一个防火墙区域（已有则跳过）
/usr/bin/rsagent tailscale start >/dev/null 2>&1 || true
/etc/init.d/rsagent enable
# 首次安装时服务还没运行，restart 里的 stop 会报一行无害的错误
/etc/init.d/rsagent restart >/dev/null 2>&1
exit 0
`

const rsagentPrerm = `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/etc/init.d/rsagent stop >/dev/null 2>&1
/etc/init.d/rsagent disable >/dev/null 2>&1
exit 0
`

const photonicatPostinst = `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
# 在 Photonicat 2 的小屏幕上加入 RemoteSerial 页（屏幕程序未运行时只给出提示，不让安装失败）
/usr/bin/rsagent screen photonicat install || echo "rsagent-photonicat: could not add the screen page now; run: rsagent screen photonicat install"
exit 0
`

const photonicatPrerm = `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
# 升级时不移除：新版的安装后脚本会原地更新这一页，免得屏幕程序重启两次
[ "$1" = upgrade ] && exit 0
/usr/bin/rsagent screen photonicat remove >/dev/null 2>&1
exit 0
`

const photonicatNote = `This package adds a RemoteSerial page to the Photonicat 2 screen.
It is set up by "rsagent screen photonicat install" and removed by "rsagent screen photonicat remove".
`

const luciRefresh = `#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache
/etc/init.d/rpcd reload >/dev/null 2>&1
exit 0
`

type file struct {
	path string // 安装后的绝对路径
	mode int64
	data []byte
}

type pkg struct {
	name, arch, description, section string
	depends                          []string
	files                            []file
	scripts                          map[string]string // ipk 脚本名（postinst、prerm、postrm）→ 内容
}

func main() {
	version := flag.String("version", "", "package version, e.g. 0.6.0")
	dist := flag.String("dist", "dist", "directory with rsagent-linux-<arch> and the output")
	luciDir := flag.String("luci", "build/luci", "LuCI file tree from make luci")
	noAPK := flag.Bool("no-apk", false, "skip .apk (needs docker)")
	image := flag.String("apk-image", "alpine:3.23", "image that provides apk mkpkg (apk-tools v3)")
	flag.Parse()
	if *version == "" {
		fail(fmt.Errorf("-version is required"))
	}
	ver, apkVer := pkgVersion(*version)
	out := *dist
	if err := os.MkdirAll(out, 0o755); err != nil {
		fail(err)
	}

	var pkgs []pkg
	for _, goarch := range []string{"amd64", "arm64"} {
		bin, err := os.ReadFile(filepath.Join(*dist, "rsagent-linux-"+goarch))
		if err != nil {
			fail(fmt.Errorf("%w (run make linux-%s first)", err, goarch))
		}
		pkgs = append(pkgs, pkg{
			name: "rsagent", arch: arches[goarch], section: "net",
			description: "RemoteSerial agent: serial-to-TCP bridges over Tailscale",
			depends:     rsagentDepends,
			files: []file{
				{"/usr/bin/rsagent", 0o755, bin},
				{deploy.ProcdInitScript, 0o755, []byte(deploy.ProcdScript(deploy.Options{DataDir: dataDir}))},
				{"/lib/upgrade/keep.d/rsagent", 0o644, []byte(dataDir + "/\n")},
			},
			scripts: map[string]string{"postinst": rsagentPostinst, "prerm": rsagentPrerm},
		})
	}
	pkgs = append(pkgs, pkg{
		name: "rsagent-photonicat", arch: "all", section: "utils",
		description: "RemoteSerial status page on the Photonicat 2 screen",
		depends:     []string{"rsagent", "pcat2-display-mini"},
		files:       []file{{"/usr/share/rsagent/photonicat-screen", 0o644, []byte(photonicatNote)}},
		scripts:     map[string]string{"postinst": photonicatPostinst, "prerm": photonicatPrerm},
	})
	luciFiles, err := readTree(*luciDir)
	if err != nil {
		fail(fmt.Errorf("%w (run make luci first)", err))
	}
	pkgs = append(pkgs, pkg{
		name: "luci-app-rsagent", arch: "all", section: "luci",
		description: "LuCI page for RemoteSerial (rsagent)",
		depends:     []string{"rsagent", "luci-base"},
		files:       luciFiles,
		scripts:     map[string]string{"postinst": luciRefresh, "postrm": luciRefresh},
	})

	for _, p := range pkgs {
		name := fmt.Sprintf("%s_%s_%s.ipk", p.name, ver, p.arch)
		if err := os.WriteFile(filepath.Join(out, name), buildIPK(p, ver), 0o644); err != nil {
			fail(err)
		}
		fmt.Println(filepath.Join(out, name))
	}
	if !*noAPK {
		if err := buildAPKs(pkgs, apkVer, out, *image); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// pkgVersion 把发布版本变成套件版本：0.6.0 → 0.6.0-r1。预发布版要排在正式版之前，两种格式写法不同：
// opkg 用 ~（0.6.0~pre3-r1 < 0.6.0-r1），apk 不接受 ~，用 _pre（0.6.0_pre3-r1 < 0.6.0-r1）。
// 后缀里的数字保留下来（0.6.1-dev.3 → pre3），每次测试构建的版本才不同，套件管理器才会真的安装。
func pkgVersion(v string) (ipk, apk string) {
	v = strings.TrimPrefix(v, "v")
	base, suffix, _ := strings.Cut(v, "-")
	if !regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`).MatchString(base) {
		fail(fmt.Errorf("version %q must look like 1.2.3", v))
	}
	if suffix == "" {
		return base + "-r1", base + "-r1"
	}
	n := preNumber(suffix)
	return base + "~pre" + n + "-r1", base + "_pre" + n + "-r1"
}

// preNumber 取出预发布后缀里的数字：dev.3 → 3，rc12 → 12，dev → ""。
func preNumber(suffix string) string {
	return strings.Join(regexp.MustCompile(`[0-9]+`).FindAllString(suffix, -1), "")
}

func readTree(root string) ([]file, error) {
	var out []file
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		mode := int64(0o644)
		if info, _ := d.Info(); info != nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		out = append(out, file{"/" + filepath.ToSlash(rel), mode, data})
		return nil
	})
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("%s is empty", root)
	}
	return out, err
}

// ---------------------------------------------------------------- ipk

var epoch = time.Unix(0, 0)

type tarEntry struct {
	name string
	mode int64
	data []byte
	dir  bool
}

func tarGz(entries []tarEntry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, ModTime: epoch, Uname: "root", Gname: "root", Format: tar.FormatGNU}
		if e.dir {
			h.Typeflag = tar.TypeDir
		} else {
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(e.data))
		}
		_ = tw.WriteHeader(h)
		if !e.dir {
			_, _ = tw.Write(e.data)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// dataEntries 列出文件与它们的上级目录（./usr/、./usr/bin/ …），按路径排序。
func dataEntries(files []file) []tarEntry {
	dirs := map[string]bool{}
	for _, f := range files {
		for d := filepath.Dir(f.path); d != "/" && d != "."; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	var out []tarEntry
	for d := range dirs {
		out = append(out, tarEntry{name: "." + d + "/", mode: 0o755, dir: true})
	}
	for _, f := range files {
		out = append(out, tarEntry{name: "." + f.path, mode: f.mode, data: f.data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return append([]tarEntry{{name: "./", mode: 0o755, dir: true}}, out...)
}

func buildIPK(p pkg, ver string) []byte {
	size := 0
	for _, f := range p.files {
		size += len(f.data)
	}
	var ctl strings.Builder
	fmt.Fprintf(&ctl, "Package: %s\nVersion: %s\n", p.name, ver)
	if len(p.depends) > 0 {
		fmt.Fprintf(&ctl, "Depends: %s\n", strings.Join(p.depends, ", "))
	}
	fmt.Fprintf(&ctl, "Source: %s\nSection: %s\nURL: %s\nMaintainer: %s\nArchitecture: %s\nInstalled-Size: %d\nDescription:  %s\n",
		homepage, p.section, homepage, maintainer, p.arch, size, p.description)
	control := []tarEntry{{name: "./", mode: 0o755, dir: true}, {name: "./control", mode: 0o644, data: []byte(ctl.String())}}
	names := make([]string, 0, len(p.scripts))
	for n := range p.scripts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		control = append(control, tarEntry{name: "./" + n, mode: 0o755, data: []byte(p.scripts[n])})
	}
	return tarGz([]tarEntry{
		{name: "./debian-binary", mode: 0o644, data: []byte("2.0\n")},
		{name: "./data.tar.gz", mode: 0o644, data: tarGz(dataEntries(p.files))},
		{name: "./control.tar.gz", mode: 0o644, data: tarGz(control)},
	})
}

// ---------------------------------------------------------------- apk

// apk 的脚本名与 ipk 不同，升级时也要跑安装后脚本。
var apkScripts = map[string][]string{
	"postinst": {"post-install", "post-upgrade"},
	"prerm":    {"pre-deinstall"},
	"postrm":   {"post-deinstall"},
}

func buildAPKs(pkgs []pkg, ver, out, image string) error {
	// 放在仓库的 build/ 下：docker（OrbStack、Docker Desktop）一定能挂载
	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("build", "owpkg-")
	if err != nil {
		return err
	}
	if stage, err = filepath.Abs(stage); err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	var sh strings.Builder
	sh.WriteString("set -e\ncp -a /stage /tmp/s && chown -R 0:0 /tmp/s && cd /tmp/s\n")
	for i, p := range pkgs {
		arch := p.arch
		if arch == "all" {
			arch = "noarch"
		}
		dir := filepath.Join(stage, fmt.Sprintf("p%d", i))
		for _, f := range p.files {
			dst := filepath.Join(dir, "root", filepath.FromSlash(f.path))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, f.data, os.FileMode(f.mode)); err != nil {
				return err
			}
		}
		args := []string{"apk", "mkpkg",
			"--info", "name:" + p.name, "--info", "version:" + ver, "--info", "arch:" + arch,
			"--info", "description:" + p.description, "--info", "url:" + homepage, "--info", "maintainer:" + maintainer,
		}
		if len(p.depends) > 0 {
			args = append(args, "--info", "depends:"+strings.Join(p.depends, " "))
		}
		for n, body := range p.scripts {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o755); err != nil {
				return err
			}
			for _, an := range apkScripts[n] {
				args = append(args, "--script", fmt.Sprintf("%s:p%d/%s", an, i, n))
			}
		}
		name := fmt.Sprintf("%s-%s_%s.apk", p.name, ver, p.arch)
		args = append(args, "--files", fmt.Sprintf("p%d/root", i), "--output", "/out/"+name)
		for _, a := range args {
			sh.WriteString(shq(a) + " ")
		}
		sh.WriteString("\necho " + shq(filepath.Join(out, name)) + "\n")
	}
	absOut, _ := filepath.Abs(out)
	cmd := exec.Command("docker", "run", "--rm", "-v", stage+":/stage:ro", "-v", absOut+":/out", image, "sh", "-c", sh.String())
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apk mkpkg in %s: %w", image, err)
	}
	return nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
