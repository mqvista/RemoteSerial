package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"rsagent/internal/bridge"
)

// 网页终端协议（/ws/term/{id}?mode=rw|ro）：
//   - 服务端 → 客户端：二进制帧是串口输出；文本帧是 JSON 状态 {"type":"status",...}
//   - 客户端 → 服务端：二进制帧写入串口（rw 模式）；文本帧是控制命令
//     {"type":"break"} {"type":"dtr","on":true} {"type":"rts","on":false}
// 连接建立时先发最近 64KB 的输出作为回显。

type termControl struct {
	Type string `json:"type"`
	On   bool   `json:"on"`
}

type termStatus struct {
	Type       string `json:"type"`
	State      string `json:"state"`
	DeviceOpen bool   `json:"deviceOpen"`
	Active     string `json:"active"`
	DTR        bool   `json:"dtr"`
	RTS        bool   `json:"rts"`
	Client     string `json:"client,omitempty"`
	Viewers    int    `json:"viewers"`
	ReadWrite  bool   `json:"readWrite"`
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.deps.Bridges == nil {
		http.Error(w, "桥接模块未初始化", http.StatusServiceUnavailable)
		return
	}
	b, ok := s.deps.Bridges.Get(id)
	if !ok {
		http.Error(w, "桥接不存在", http.StatusNotFound)
		return
	}
	rw := r.URL.Query().Get("mode") == "rw"
	who := requesterLabel(r)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s.deps.Log.Info("网页终端已连接", "bridge", id, "rw", rw, "by", who)
	defer s.deps.Log.Info("网页终端已断开", "bridge", id, "by", who)

	out, unsub := b.Subscribe()
	defer unsub()

	writeBin := func(p []byte) error {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		return c.Write(wctx, websocket.MessageBinary, p)
	}
	sendStatus := func(last *termStatus) error {
		st := b.Status()
		ts := termStatus{
			Type: "status", State: string(st.State), DeviceOpen: st.DeviceOpen, Active: st.Active,
			DTR: st.DTR, RTS: st.RTS, Client: st.Client, Viewers: st.Viewers, ReadWrite: rw,
		}
		if last != nil && *last == ts {
			return nil
		}
		*last = ts
		data, _ := json.Marshal(ts)
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		return c.Write(wctx, websocket.MessageText, data)
	}

	var last termStatus
	if err := sendStatus(&last); err != nil {
		return
	}
	if recent := b.Recent(); len(recent) > 0 {
		if err := writeBin(recent); err != nil {
			return
		}
	}

	// 读取客户端输入
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if !rw {
					continue
				}
				if err := b.WriteDevice(data); err != nil {
					s.deps.Log.Debug("终端写入串口失败", "bridge", id, "err", err)
				}
			case websocket.MessageText:
				var cmd termControl
				if json.Unmarshal(data, &cmd) != nil {
					continue
				}
				if !rw && cmd.Type != "status" {
					continue
				}
				switch cmd.Type {
				case "break":
					_ = b.Break(250 * time.Millisecond)
				case "dtr":
					_ = b.SetDTR(cmd.On)
					s.hub.Notify()
				case "rts":
					_ = b.SetRTS(cmd.On)
					s.hub.Notify()
				}
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-out:
			// 合并短时间内到达的多个块，减少帧数
			batch := data
			for drained := false; !drained; {
				select {
				case more := <-out:
					batch = append(batch, more...)
					if len(batch) > 32<<10 {
						drained = true
					}
				default:
					drained = true
				}
			}
			if err := writeBin(batch); err != nil {
				return
			}
		case <-ticker.C:
			if err := sendStatus(&last); err != nil {
				return
			}
		}
	}
}

var _ = bridge.StateConnected
