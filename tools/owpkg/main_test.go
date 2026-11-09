package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestPkgVersion(t *testing.T) {
	for in, want := range map[string][2]string{
		"0.6.0":     {"0.6.0-r1", "0.6.0-r1"},
		"v1.2.3":    {"1.2.3-r1", "1.2.3-r1"},
		"0.6.0-dev": {"0.6.0~pre-r1", "0.6.0_pre-r1"},
	} {
		ipk, apk := pkgVersion(in)
		if ipk != want[0] || apk != want[1] {
			t.Errorf("%s: got %s / %s, want %v", in, ipk, apk, want)
		}
	}
}

func untar(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Uid != 0 || h.Gid != 0 {
			t.Errorf("%s owned by %d:%d", h.Name, h.Uid, h.Gid)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

// .ipk 的结构与 ipkg-build 相同：外层 tar.gz 里依次是 debian-binary、data.tar.gz、control.tar.gz。
func TestBuildIPK(t *testing.T) {
	p := pkg{name: "rsagent", arch: "aarch64_generic", section: "net", description: "d",
		depends: []string{"tailscale", "kmod-usb-acm"},
		files:   []file{{"/usr/bin/rsagent", 0o755, []byte("bin")}},
		scripts: map[string]string{"postinst": "#!/bin/sh\n"}}
	outer := untar(t, buildIPK(p, "0.6.0-r1"))
	if string(outer["./debian-binary"]) != "2.0\n" {
		t.Fatalf("debian-binary: %q", outer["./debian-binary"])
	}
	ctl := untar(t, outer["./control.tar.gz"])
	c := string(ctl["./control"])
	for _, want := range []string{"Package: rsagent\n", "Version: 0.6.0-r1\n", "Depends: tailscale, kmod-usb-acm\n", "Architecture: aarch64_generic\n", "Installed-Size: 3\n"} {
		if !strings.Contains(c, want) {
			t.Errorf("control lacks %q:\n%s", want, c)
		}
	}
	if _, ok := ctl["./postinst"]; !ok {
		t.Error("no postinst")
	}
	data := untar(t, outer["./data.tar.gz"])
	if string(data["./usr/bin/rsagent"]) != "bin" {
		t.Error("binary missing from data.tar.gz")
	}
	if _, ok := data["./usr/bin/"]; !ok {
		t.Error("parent directories missing")
	}
}
