package bridge

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"time"

	"rsagent/internal/config"
)

func TestSampleRate(t *testing.T) {
	b := &Bridge{}
	t0 := time.Unix(1000, 0)
	if b.sampleRate(t0) {
		t.Fatal("first sample should only record")
	}
	b.fromDevice.Add(11520)
	b.toDevice.Add(300)
	if !b.sampleRate(t0.Add(time.Second)) {
		t.Fatal("rate change not reported")
	}
	if f, to := b.rates(); f != 11520 || to != 300 {
		t.Fatalf("rates %d %d", f, to)
	}
	// 2 秒内又收 2000 字节：1000 B/s
	b.fromDevice.Add(2000)
	b.sampleRate(t0.Add(3 * time.Second))
	if f, to := b.rates(); f != 1000 || to != 0 {
		t.Fatalf("rates %d %d", f, to)
	}
	// 没有流量：降回 0 也要通知
	if !b.sampleRate(t0.Add(4*time.Second)) || b.sampleRate(t0.Add(5*time.Second)) {
		t.Fatal("drop to zero should be reported once")
	}
}

type bufClient struct{ bytes.Buffer }

func (c *bufClient) Close() error { return nil }

func TestWriteClient(t *testing.T) {
	b := newBridge(config.Bridge{ID: "t"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err := b.WriteClient([]byte("x")); err == nil {
		t.Fatal("no client should be an error")
	}
	c := &bufClient{}
	b.client = c
	hist, _, unsub := b.Subscribe()
	unsub()
	if len(hist) != 0 {
		t.Fatal("unexpected history")
	}
	if err := b.WriteClient([]byte("hello\r\n")); err != nil {
		t.Fatal(err)
	}
	if c.String() != "hello\r\n" {
		t.Fatalf("client got %q", c.String())
	}
	hist, _, unsub = b.Subscribe()
	unsub()
	if len(hist) != 1 || hist[0].Dir != DirRXWeb || string(hist[0].Data) != "hello\r\n" {
		t.Fatalf("history %+v", hist)
	}
	if b.fromDevice.Load() != 0 {
		t.Fatal("client writes must not count as serial traffic")
	}
}
