package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPackageNames(t *testing.T) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64_generic"}[runtime.GOARCH]
	if arch == "" {
		t.Skip("no OpenWrt package for this GOARCH")
	}
	got := packageNames("opkg", "0.6.1", true)
	want := []string{"rsagent_0.6.1-r1_" + arch + ".ipk", "luci-app-rsagent_0.6.1-r1_all.ipk"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("opkg: %v, want %v", got, want)
	}
	got = packageNames("apk", "0.7.0-rc1", false)
	if len(got) != 1 || got[0] != "rsagent-0.7.0_pre1-r1_"+arch+".apk" {
		t.Errorf("apk: %v", got)
	}
	if packageNames("opkg", "dev", false) != nil {
		t.Error("dev should have no package")
	}
}

func TestLatestPackages(t *testing.T) {
	m := &Manifest{Version: "0.6.1", Files: map[string]Asset{}}
	if _, err := latestPackages(m, "opkg"); err == nil {
		t.Fatal("missing package accepted")
	}
	for _, n := range packageNames("opkg", "0.6.1", false) {
		m.Files[n] = Asset{URL: "https://x/" + n, SHA256: "AB", Size: 3}
	}
	p, err := latestPackages(m, "opkg")
	if err != nil || len(p) != 1 || p[0].SHA256 != "ab" {
		t.Fatalf("%v %v", p, err)
	}
}

func TestReadApplyLog(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := readApplyLog(dir); ok {
		t.Fatal("no log yet")
	}
	log := "version=0.6.1\nInstalling rsagent\n * opkg_install_cmd: Cannot install package rsagent.\nexit=255\n"
	if err := os.WriteFile(filepath.Join(dir, "pkg-apply.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	code, tail, ok := readApplyLog(dir)
	if !ok || code != 255 || tail != "Installing rsagent / * opkg_install_cmd: Cannot install package rsagent." {
		t.Fatalf("%d %q %v", code, tail, ok)
	}
}

func TestRemovedDrivers(t *testing.T) {
	log := "Upgrading rsagent on root from 0.6.1-r1 to 0.6.2-r1...\n" +
		"kmod-usb-acm was autoinstalled and is now orphaned, removing.\n" +
		"kmod-usb-serial-ch341 was autoinstalled and is now orphaned, removing.\n" +
		"(2/7) Purging kmod-usb-serial-ftdi (6.12.112-r1)\n" +
		"(3/7) Purging kmod-usb-core (6.12.112-r1)\n" + // 不是串口驱动，不装回
		"exit=0\n"
	got := removedDrivers(log)
	want := []string{"kmod-usb-acm", "kmod-usb-serial-ch341", "kmod-usb-serial-ftdi"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
