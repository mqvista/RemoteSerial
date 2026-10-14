package bridge

import "sync"

// recentRing 保存最近 N 字节的串口输出。
type recentRing struct {
	mu   sync.Mutex
	buf  []byte
	w    int
	full bool
}

func newRecentRing(n int) *recentRing { return &recentRing{buf: make([]byte, n)} }

func (r *recentRing) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.w = 0
		r.full = true
		return
	}
	n := copy(r.buf[r.w:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.w = (r.w + len(p)) % len(r.buf)
	if r.w == 0 {
		r.full = true
	}
}

// Bytes 按时间顺序返回内容副本。
func (r *recentRing) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]byte(nil), r.buf[:r.w]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.w:]...)
	out = append(out, r.buf[:r.w]...)
	return out
}
