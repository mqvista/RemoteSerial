package main

import "testing"

// 期望值取自 OpenWrt 24.10 的 base.zh-tw.lmo 中对应条目的键。
func TestSFHHash(t *testing.T) {
	for s, want := range map[string]uint32{
		"Save":     0x219966c7,
		"Status":   0x663359c9,
		"Services": 0x246f4de5,
		"Start":    0x95660ce9,
		"Network":  0xe54695b9,
	} {
		if got := sfhHash([]byte(s)); got != want {
			t.Errorf("%q: got %#x, want %#x", s, got, want)
		}
	}
}
