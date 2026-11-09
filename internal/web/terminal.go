package web

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"rsagent/internal/i18n"
	"time"

	"github.com/coder/websocket"

	"rsagent/internal/bridge"
)

// 网页终端协议（/ws/term/{id}?mode=rw|ro）：
//   - 服务端 → 客户端：二进制帧是一条或多条流量记录，每条为
//     方向(1 字节：0 RX 设备输出，1 TX TCP 客户端写入，2 TX 网页终端写入串口，3 RX 网页终端发给 TCP 客户端)
//     + 时间(8 字节，Unix 微秒，大端) + 长度(4 字节，大端) + 数据；
//     文本帧是 JSON 状态 {"type":"status",...}
//   - 客户端 → 服务端：二进制帧写入串口（rw 模式）；文本帧是控制命令
//     {"type":"break"} {"type":"dtr","on":true} {"type":"rts","on":false}
//     {"type":"client","data":"<base64>"} 把数据发给当前连接的 TCP 客户端（网络端）
// 连接建立时先发最近约 64KB 的双向历史。

const termRecordHeader = 13

// appendRecord 把一块流量编码进终端帧。
func appendRecord(dst []byte, c bridge.Chunk) []byte {
	var h [termRecordHeader]byte
	h[0] = byte(c.Dir)
	binary.BigEndian.PutUint64(h[1:9], uint64(c.Time.UnixMicro()))
	binary.BigEndian.PutUint32(h[9:13], uint32(len(c.Data)))
	return append(append(dst, h[:]...), c.Data...)
}

// termFrameLimit 是合并多条记录时单帧的大致上限。
const termFrameLimit = 32 << 10

type termControl struct {
	Type string `json:"type"`
	On   bool   `json:"on"`
	Data []byte `json:"data,omitempty"` // client：要发给 TCP 客户端的数据（base64）
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
		http.Error(w, i18n.T("桥接模块未初始化"), http.StatusServiceUnavailable)
		return
	}
	b, ok := s.deps.Bridges.Get(id)
	if !ok {
		http.Error(w, i18n.T("桥接不存在"), http.StatusNotFound)
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

	s.deps.Log.Info(i18n.T("网页终端已连接"), "bridge", id, "rw", rw, "by", who)
	defer s.deps.Log.Info(i18n.T("网页终端已断开"), "bridge", id, "by", who)

	hist, out, unsub := b.Subscribe()
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
	var frame []byte
	for _, c := range hist {
		frame = appendRecord(frame, c)
		if len(frame) >= termFrameLimit {
			if err := writeBin(frame); err != nil {
				return
			}
			frame = frame[:0]
		}
	}
	if len(frame) > 0 {
		if err := writeBin(frame); err != nil {
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
					s.deps.Log.Debug(i18n.T("终端写入串口失败"), "bridge", id, "err", err)
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
				case "client":
					if err := b.WriteClient(cmd.Data); err != nil {
						s.deps.Log.Debug(i18n.T("终端写入 TCP 客户端失败"), "bridge", id, "err", err)
					}
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
		case c := <-out:
			// 合并已经到达的多条记录，减少帧数；每条仍保留自己的方向与时间
			frame := appendRecord(nil, c)
			for drained := false; !drained && len(frame) < termFrameLimit; {
				select {
				case more := <-out:
					frame = appendRecord(frame, more)
				default:
					drained = true
				}
			}
			if err := writeBin(frame); err != nil {
				return
			}
		case <-ticker.C:
			if err := sendStatus(&last); err != nil {
				return
			}
		}
	}
}
