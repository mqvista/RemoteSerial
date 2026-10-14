package bridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// capture 把串口双向流量原样写入两个文件：<base>.rx.bin（设备 → 外）和 <base>.tx.bin（外 → 设备）。
type capture struct {
	mu      sync.Mutex
	base    string
	rx, tx  *os.File
	started time.Time
	rxBytes int64
	txBytes int64
}

// CaptureStatus 是录制状态。
type CaptureStatus struct {
	Active  bool      `json:"active"`
	Base    string    `json:"base,omitempty"` // 文件名前缀，不含目录
	Started time.Time `json:"started,omitzero"`
	RxBytes int64     `json:"rxBytes"`
	TxBytes int64     `json:"txBytes"`
}

// StartCapture 在 dir 下开始录制，返回文件名前缀。
func (b *Bridge) StartCapture(dir string) (string, error) {
	b.mu.Lock()
	if b.cap != nil {
		b.mu.Unlock()
		return "", errors.New("已在录制")
	}
	b.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := fmt.Sprintf("capture-%s-%s", b.cfg.ID, time.Now().Format("20060102-150405"))
	rx, err := os.OpenFile(filepath.Join(dir, base+".rx.bin"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	tx, err := os.OpenFile(filepath.Join(dir, base+".tx.bin"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		rx.Close()
		return "", err
	}
	c := &capture{base: base, rx: rx, tx: tx, started: time.Now()}
	b.mu.Lock()
	b.cap = c
	b.mu.Unlock()
	b.log.Info("开始录制串口流量", "base", base)
	b.notify()
	return base, nil
}

// StopCapture 停止录制。
func (b *Bridge) StopCapture() error {
	b.mu.Lock()
	c := b.cap
	b.cap = nil
	b.mu.Unlock()
	if c == nil {
		return errors.New("未在录制")
	}
	c.mu.Lock()
	_ = c.rx.Close()
	_ = c.tx.Close()
	c.mu.Unlock()
	b.log.Info("停止录制串口流量", "base", c.base, "rxBytes", c.rxBytes, "txBytes", c.txBytes)
	b.notify()
	return nil
}

// Capture 返回录制状态。
func (b *Bridge) Capture() CaptureStatus {
	b.mu.Lock()
	c := b.cap
	b.mu.Unlock()
	if c == nil {
		return CaptureStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CaptureStatus{Active: true, Base: c.base, Started: c.started, RxBytes: c.rxBytes, TxBytes: c.txBytes}
}

func (b *Bridge) captureRx(p []byte) {
	b.mu.Lock()
	c := b.cap
	b.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	n, _ := c.rx.Write(p)
	c.rxBytes += int64(n)
	c.mu.Unlock()
}

func (b *Bridge) captureTx(p []byte) {
	b.mu.Lock()
	c := b.cap
	b.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	n, _ := c.tx.Write(p)
	c.txBytes += int64(n)
	c.mu.Unlock()
}
