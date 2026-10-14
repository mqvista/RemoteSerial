// mkmanifest 为一次发布生成自更新清单 manifest.json 和 SHA256SUMS。
//
// 用法：
//
//	go run ./tools/mkmanifest -version 0.4.0 -dir dist \
//	    -base https://github.com/mqvista/RemoteSerial/releases/download/v0.4.0 \
//	    [-notes-file CHANGELOG.md] [-out dist/manifest.json]
//
// dir 下形如 rsagent-<os>-<arch>[.exe] 的文件进入 assets，键为 <os>-<arch>；
// 目录里的其他文件（安装包、deb）只进 SHA256SUMS。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	Version     string           `json:"version"`
	Notes       string           `json:"notes,omitempty"`
	PublishedAt string           `json:"publishedAt"`
	Assets      map[string]asset `json:"assets"`
	Files       map[string]asset `json:"files,omitempty"` // 安装包等其他发布文件
}

var binRe = regexp.MustCompile(`^rsagent-(windows|linux|darwin)-(amd64|arm64|386|arm)(\.exe)?$`)

func main() {
	version := flag.String("version", "", "版本号，如 0.4.0")
	dir := flag.String("dir", "dist", "发布文件目录")
	base := flag.String("base", "", "下载地址前缀，文件名直接拼在后面")
	notesFile := flag.String("notes-file", "", "发布说明文件")
	out := flag.String("out", "", "清单输出路径，默认 <dir>/manifest.json")
	flag.Parse()
	if *version == "" || *base == "" {
		fmt.Fprintln(os.Stderr, "需要 -version 和 -base")
		os.Exit(2)
	}
	if *out == "" {
		*out = filepath.Join(*dir, "manifest.json")
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		fatal(err)
	}
	m := manifest{Version: strings.TrimPrefix(*version, "v"), PublishedAt: time.Now().UTC().Format(time.RFC3339), Assets: map[string]asset{}, Files: map[string]asset{}}
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			fatal(err)
		}
		m.Notes = strings.TrimSpace(string(b))
	}
	var sums []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "manifest.json" || name == "SHA256SUMS" {
			continue
		}
		sum, size, err := hashFile(filepath.Join(*dir, name))
		if err != nil {
			fatal(err)
		}
		sums = append(sums, sum+"  "+name)
		a := asset{URL: strings.TrimRight(*base, "/") + "/" + name, SHA256: sum, Size: size}
		if mm := binRe.FindStringSubmatch(name); mm != nil {
			m.Assets[mm[1]+"-"+mm[2]] = a
		} else {
			m.Files[name] = a
		}
	}
	if len(m.Assets) == 0 {
		fatal(fmt.Errorf("%s 下没有 rsagent-<os>-<arch> 程序文件", *dir))
	}
	sort.Strings(sums)
	if err := os.WriteFile(filepath.Join(*dir, "SHA256SUMS"), []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		fatal(err)
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("已生成 %s（%d 个平台）和 SHA256SUMS（%d 个文件）\n", *out, len(m.Assets), len(sums))
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
