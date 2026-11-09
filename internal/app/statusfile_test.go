package app

import "testing"

func TestFormatRate(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B/s", 999: "999 B/s", 1000: "1.0 KB/s", 11571: "11.3 KB/s", 1023897: "999.9 KB/s", 1023949: "1.0 MB/s", 5 << 20: "5.0 MB/s"} {
		if got := formatRate(n); got != want {
			t.Errorf("%d: got %q, want %q", n, got, want)
		}
	}
}
