package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry 是环形缓冲中的一条日志，供管理界面读取。
type Entry struct {
	Time  time.Time  `json:"time"`
	Lvl   slog.Level `json:"-"`
	Level string     `json:"level"`
	Msg   string     `json:"msg"`
	Attrs string     `json:"attrs,omitempty"`
}

// Ring 是固定容量的内存日志环。
type Ring struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	full bool
}

// NewRing 创建容量为 n 的环。
func NewRing(n int) *Ring {
	if n <= 0 {
		n = 1000
	}
	return &Ring{buf: make([]Entry, n)}
}

// Add 追加一条，满了覆盖最旧的。
func (r *Ring) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Last 返回级别不低于 min 的最新 n 条，按时间先后排列。
func (r *Ring) Last(n int, min slog.Level) []Entry {
	r.mu.Lock()
	var all []Entry
	if r.full {
		all = make([]Entry, 0, len(r.buf))
		all = append(all, r.buf[r.next:]...)
		all = append(all, r.buf[:r.next]...)
	} else {
		all = append([]Entry(nil), r.buf[:r.next]...)
	}
	r.mu.Unlock()

	out := make([]Entry, 0, len(all))
	for _, e := range all {
		if e.Lvl >= min {
			out = append(out, e)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// ringHandler 把 slog 记录写入 Ring。
type ringHandler struct {
	ring  *Ring
	level slog.Level
	attrs []slog.Attr
	group string
}

func (h *ringHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *ringHandler) Handle(_ context.Context, rec slog.Record) error {
	var sb strings.Builder
	for _, a := range h.attrs {
		writeAttr(&sb, h.group, a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		writeAttr(&sb, h.group, a)
		return true
	})
	h.ring.Add(Entry{
		Time:  rec.Time,
		Lvl:   rec.Level,
		Level: rec.Level.String(),
		Msg:   rec.Message,
		Attrs: strings.TrimSpace(sb.String()),
	})
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &nh
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	nh := *h
	if name != "" {
		if nh.group != "" {
			nh.group += "."
		}
		nh.group += name
	}
	return &nh
}

func writeAttr(sb *strings.Builder, group string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	sb.WriteString(key)
	sb.WriteByte('=')
	sb.WriteString(a.Value.String())
	sb.WriteByte(' ')
}

// multiHandler 把一条记录分发给多个 handler。
type multiHandler struct{ hs []slog.Handler }

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.hs {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, rec slog.Record) error {
	var first error
	for _, h := range m.hs {
		if !h.Enabled(ctx, rec.Level) {
			continue
		}
		if err := h.Handle(ctx, rec.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		hs[i] = h.WithAttrs(attrs)
	}
	return multiHandler{hs}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.hs))
	for i, h := range m.hs {
		hs[i] = h.WithGroup(name)
	}
	return multiHandler{hs}
}
