package tsnode

import (
	"net/netip"
	"slices"
	"testing"
)

func TestMergePrefixes(t *testing.T) {
	p := func(ss ...string) []netip.Prefix {
		out := []netip.Prefix{}
		for _, s := range ss {
			out = append(out, netip.MustParsePrefix(s))
		}
		return out
	}
	// 用户自己加的 10.0.0.0/8 与出口节点保留；rsagent 去掉旧的 192.168.1.0/24，加上新的
	cur := p("10.0.0.0/8", "192.168.1.0/24", "0.0.0.0/0", "::/0")
	got := mergePrefixes(cur, p("192.168.1.0/24"), p("192.168.2.0/24"))
	want := p("10.0.0.0/8", "0.0.0.0/0", "::/0", "192.168.2.0/24")
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// 已有的条目不重复添加
	if got := mergePrefixes(p("192.168.1.0/24"), nil, p("192.168.1.0/24")); len(got) != 1 {
		t.Fatalf("duplicate added: %v", got)
	}
	if got := mergePrefixes(nil, nil, nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}
