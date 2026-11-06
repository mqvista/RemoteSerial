package i18n

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var translateFuncs = map[string]bool{"T": true, "Sprintf": true, "M": true, "Errorf": true, "New": true}

// 只扫描会被用户看到文字的代码；开发工具（tools/、cmd/tsprobe）不在双语范围内。
var scanRoots = []string{"cmd/rsagent", "internal"}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

var verbRe = regexp.MustCompile(`%[-+# 0]*(\d+|\*)?(\.(\d+|\*))?[a-zA-Z%]`)

func verbs(s string) string {
	v := verbRe.FindAllString(s, -1)
	return strings.Join(v, " ")
}

type site struct {
	src string
	pos string
}

func scan(t *testing.T) (used []site, stray []site) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, sub := range scanRoots {
		err := filepath.WalkDir(filepath.Join(root, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "catalog.go" {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			inCall := map[*ast.BasicLit]bool{}
			tags := map[*ast.BasicLit]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Field:
					if x.Tag != nil {
						tags[x.Tag] = true
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || len(x.Args) == 0 {
						return true
					}
					id, ok := sel.X.(*ast.Ident)
					// 包内调用（i18n 包自身）不经过选择器，这里只看 i18n.X(...)
					if !ok || id.Name != "i18n" || !translateFuncs[sel.Sel.Name] {
						return true
					}
					if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						inCall[lit] = true
						s, _ := strconv.Unquote(lit.Value)
						used = append(used, site{s, fset.Position(lit.Pos()).String()})
					}
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || inCall[lit] || tags[lit] {
					return true
				}
				s, _ := strconv.Unquote(lit.Value)
				if hasCJK(s) {
					stray = append(stray, site{s, fset.Position(lit.Pos()).String()})
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return used, stray
}

func TestCatalogCoversAllCallSites(t *testing.T) {
	used, stray := scan(t)
	seen := map[string]bool{}
	for _, u := range used {
		seen[u.src] = true
		e, ok := catalog[u.src]
		if !ok {
			t.Errorf("%s: 缺少译文: %q", u.pos, u.src)
			continue
		}
		if e[0] == "" || e[1] == "" {
			t.Errorf("%s: 译文为空: %q", u.pos, u.src)
		}
		if hasCJK(e[0]) {
			t.Errorf("%s: 英文译文含中文: %q", u.pos, e[0])
		}
		if verbs(e[0]) != verbs(u.src) || verbs(e[1]) != verbs(u.src) {
			t.Errorf("%s: 格式占位符不一致: %q / %q / %q", u.pos, u.src, e[0], e[1])
		}
	}
	for _, s := range stray {
		t.Errorf("%s: 中文字符串没有经过 i18n: %q", s.pos, s.src)
	}
	var unused []string
	for k := range catalog {
		if !seen[k] {
			unused = append(unused, k)
		}
	}
	sort.Strings(unused)
	for _, k := range unused {
		t.Errorf("catalog 中未使用的条目: %q", k)
	}
}

func TestLazyTranslation(t *testing.T) {
	defer SetLang(Current())
	inner := New("节点未启动")
	err := Errorf("调用运行中的 core: %w", inner)
	SetLang(EN)
	en := err.Error()
	SetLang(TW)
	tw := err.Error()
	if en == tw || hasCJK(en) || !hasCJK(tw) {
		t.Fatalf("切换语言后错误文字没有变化: %q / %q", en, tw)
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is 无法穿透 %w")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]Lang{"zh-Hant-TW": TW, "zh_CN.UTF-8": TW, "zh-Hans": TW, "en-US": EN, "en": EN, "fr": "", "": ""} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
