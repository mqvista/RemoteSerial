// luci-i18n 处理 luci-app-rsagent 的翻译，不依赖 OpenWrt SDK：
//
//	luci-i18n pot <out.pot> <file.js|file.json>...   提取 _('...') 与菜单标题，生成模板
//	luci-i18n lmo <in.po> <out.lmo>                   把 .po 编译成 LuCI 使用的 .lmo
//
// .lmo 格式与 LuCI 的 po2lmo 相同：字符串依次存放并按 4 字节对齐，末尾是按键哈希排序的索引
// （每项 key_id、val_id、offset、length，均为大端 uint32），最后 4 字节是索引的偏移。
// 键哈希是 Paul Hsieh 的 SuperFastHash，初值为长度。
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: luci-i18n pot <out.pot> <files>... | luci-i18n lmo <in.po> <out.lmo>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "pot":
		err = makePot(os.Args[2], os.Args[3:])
	case "lmo":
		err = makeLmo(os.Args[2], os.Args[3])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- pot

var jsCall = regexp.MustCompile(`\b_\(\s*'((?:[^'\\]|\\.)*)'\s*\)`)

func unescapeJS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func makePot(out string, files []string) error {
	seen := map[string]bool{}
	var ids []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if strings.HasSuffix(f, ".json") {
			// 菜单文件：各项的 title 会被翻译
			var menu map[string]struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(data, &menu); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			keys := make([]string, 0, len(menu))
			for k := range menu {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				add(menu[k].Title)
			}
			continue
		}
		for _, m := range jsCall.FindAllStringSubmatch(string(data), -1) {
			add(unescapeJS(m[1]))
		}
	}
	var b bytes.Buffer
	b.WriteString("msgid \"\"\nmsgstr \"Content-Type: text/plain; charset=UTF-8\"\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "\nmsgid %s\nmsgstr \"\"\n", poQuote(id))
	}
	return os.WriteFile(out, b.Bytes(), 0o644)
}

func poQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// ---------------------------------------------------------------- po

type entry struct{ id, str string }

// parsePo 读取简单的 .po：msgid / msgstr 及其续行，忽略注释、msgctxt 与复数形式。
func parsePo(path string) ([]entry, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	var out []entry
	var cur entry
	var field *string
	header := ""
	flush := func() {
		if cur.id == "" {
			header = cur.str
		} else if cur.str != "" {
			out = append(out, cur)
		}
		cur = entry{}
		field = nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	started := false
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "msgid "):
			if started {
				flush()
			}
			started = true
			field = &cur.id
			line = strings.TrimPrefix(line, "msgid ")
		case strings.HasPrefix(line, "msgstr "):
			field = &cur.str
			line = strings.TrimPrefix(line, "msgstr ")
		case strings.HasPrefix(line, `"`):
		default:
			return nil, "", fmt.Errorf("%s:%d: unsupported line %q", path, n, line)
		}
		if field == nil {
			return nil, "", fmt.Errorf("%s:%d: string outside msgid/msgstr", path, n)
		}
		s, err := strconv.Unquote(line)
		if err != nil {
			return nil, "", fmt.Errorf("%s:%d: %v", path, n, err)
		}
		*field += s
	}
	if started {
		flush()
	}
	return out, header, sc.Err()
}

// ---------------------------------------------------------------- lmo

func get16(b []byte, i int) uint32 { return uint32(b[i]) | uint32(b[i+1])<<8 }

func sfhHash(b []byte) uint32 {
	n := len(b)
	if n == 0 {
		return 0
	}
	h := uint32(n)
	rem := n & 3
	i := 0
	for k := n >> 2; k > 0; k-- {
		h += get16(b, i)
		tmp := (get16(b, i+2) << 11) ^ h
		h = (h << 16) ^ tmp
		i += 4
		h += h >> 11
	}
	switch rem {
	case 3:
		h += get16(b, i)
		h ^= h << 16
		h ^= uint32(int32(int8(b[i+2]))) << 18
		h += h >> 11
	case 2:
		h += get16(b, i)
		h ^= h << 11
		h += h >> 17
	case 1:
		h += uint32(int32(int8(b[i])))
		h ^= h << 10
		h += h >> 1
	}
	h ^= h << 3
	h += h >> 5
	h ^= h << 4
	h += h >> 17
	h ^= h << 25
	h += h >> 6
	return h
}

type lmoIndex struct{ key, val, off, length uint32 }

func makeLmo(in, out string) error {
	entries, header, err := parsePo(in)
	if err != nil {
		return err
	}
	var data bytes.Buffer
	var idx []lmoIndex
	put := func(key, val uint32, s string) {
		off := uint32(data.Len())
		data.WriteString(s)
		for data.Len()%4 != 0 {
			data.WriteByte(0)
		}
		idx = append(idx, lmoIndex{key, val, off, uint32(len(s))})
	}
	// 与 po2lmo 一样把复数规则作为键 0 存入
	plural := "nplurals=1; plural=0;"
	for _, l := range strings.Split(header, "\n") {
		if v, ok := strings.CutPrefix(l, "Plural-Forms:"); ok {
			plural = strings.TrimSpace(v)
		}
	}
	put(0, 0, plural)
	seen := map[uint32]string{}
	for _, e := range entries {
		k := sfhHash([]byte(e.id))
		if prev, dup := seen[k]; dup {
			if prev == e.id {
				continue
			}
			return fmt.Errorf("hash collision between %q and %q", prev, e.id)
		}
		seen[k] = e.id
		put(k, 1, e.str)
	}
	sort.Slice(idx, func(i, j int) bool { return idx[i].key < idx[j].key })
	start := uint32(data.Len())
	for _, x := range idx {
		_ = binary.Write(&data, binary.BigEndian, [4]uint32{x.key, x.val, x.off, x.length})
	}
	_ = binary.Write(&data, binary.BigEndian, start)
	return os.WriteFile(out, data.Bytes(), 0o644)
}
