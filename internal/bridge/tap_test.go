package bridge

import (
	"bytes"
	"testing"
)

func TestTapHistoryAndOrder(t *testing.T) {
	tp := newTap()
	tp.publish(DirRX, []byte("hello"))
	tp.publish(DirTX, []byte("AT\r"))
	buf := []byte("x")
	tp.publish(DirTXWeb, buf)
	buf[0] = 'y' // publish 必须复制数据

	hist, ch := tp.subscribe()
	defer tp.unsubscribe(ch)
	if len(hist) != 3 || hist[0].Dir != DirRX || hist[1].Dir != DirTX || hist[2].Dir != DirTXWeb {
		t.Fatalf("history = %+v", hist)
	}
	if string(hist[2].Data) != "x" {
		t.Fatalf("data not copied: %q", hist[2].Data)
	}
	if hist[1].Time.Before(hist[0].Time) || hist[2].Time.Before(hist[1].Time) {
		t.Fatal("timestamps out of order")
	}
	tp.publish(DirRX, []byte("live"))
	if c := <-ch; c.Dir != DirRX || string(c.Data) != "live" {
		t.Fatalf("live = %+v", c)
	}
	if tp.count() != 1 {
		t.Fatalf("count = %d", tp.count())
	}
}

func TestTapLimits(t *testing.T) {
	tp := newTap()
	for i := 0; i < historyChunks*2; i++ {
		tp.publish(DirRX, []byte{byte(i)})
	}
	hist, _ := tp.subscribe()
	if len(hist) != historyChunks {
		t.Fatalf("chunks = %d, want %d", len(hist), historyChunks)
	}
	if hist[len(hist)-1].Data[0] != byte((historyChunks*2-1)%256) {
		t.Fatal("newest chunk not kept")
	}

	tp = newTap()
	big := bytes.Repeat([]byte("a"), 40<<10)
	tp.publish(DirRX, big)
	tp.publish(DirTX, big)
	hist, _ = tp.subscribe()
	if len(hist) != 1 || hist[0].Dir != DirTX {
		t.Fatalf("byte limit: %d chunks", len(hist))
	}
	// 单块超过上限时仍保留最新一块
	tp.publish(DirRX, bytes.Repeat([]byte("b"), historyBytes+1))
	hist, _ = tp.subscribe()
	if len(hist) != 1 || hist[0].Dir != DirRX {
		t.Fatalf("oversized: %d chunks", len(hist))
	}
}
