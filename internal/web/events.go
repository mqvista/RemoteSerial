package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Hub 把状态变化广播给所有 /ws/events 连接。
type Hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewHub 创建广播中心。
func NewHub() *Hub { return &Hub{subs: map[chan struct{}]struct{}{}} }

// Notify 通知所有订阅者"状态有变化"。订阅者收到后自己拉取最新状态，
// 这样多次变化会被合并，不会堆积消息。
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// Event 是推送给前端的消息。
type Event struct {
	Type   string         `json:"type"` // status
	Status StatusResponse `json:"status"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// webview 和浏览器的 Origin 与 Host 一致；Vite 开发服务器通过代理访问时 Origin 是 localhost:5173。
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx := c.CloseRead(r.Context())
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	send := func() error {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return wsjson.Write(wctx, c, Event{Type: "status", Status: s.buildStatus(r)})
	}
	if err := send(); err != nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-ticker.C:
		}
		if err := send(); err != nil {
			return
		}
	}
}
