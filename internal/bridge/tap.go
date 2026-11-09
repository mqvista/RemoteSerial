package bridge

import (
	"sync"
	"time"
)

// Dir 是数据方向。
type Dir byte

const (
	DirRX    Dir = 0 // 串口 → 外（设备输出）
	DirTX    Dir = 1 // TCP 客户端 → 串口
	DirTXWeb Dir = 2 // 网页终端 → 串口
	DirRXWeb Dir = 3 // 网页终端 → TCP 客户端（模拟设备输出）
)

// Chunk 是一次读写的数据，带方向与时间，供网页终端监看。
type Chunk struct {
	Dir  Dir
	Time time.Time
	Data []byte
}

const (
	historyBytes  = 64 << 10 // 回显历史的数据量上限
	historyChunks = 4096     // 以及块数上限，避免大量单字节块占用过多内存
)

// tap 按发生顺序保存最近的双向流量，并分发给订阅者。
// 记录与分发在同一把锁里完成，订阅者看到的顺序与历史一致。
type tap struct {
	mu    sync.Mutex
	hist  []Chunk
	bytes int
	subs  map[chan Chunk]struct{}
}

func newTap() *tap { return &tap{subs: map[chan Chunk]struct{}{}} }

// publish 复制 p 后记录并分发。慢订阅者丢数据而不是阻塞串口读写。
func (t *tap) publish(d Dir, p []byte) {
	if len(p) == 0 {
		return
	}
	c := Chunk{Dir: d, Data: append([]byte(nil), p...)}
	t.mu.Lock()
	defer t.mu.Unlock()
	c.Time = time.Now()
	t.hist = append(t.hist, c)
	t.bytes += len(c.Data)
	drop := 0
	for drop < len(t.hist)-1 && (t.bytes > historyBytes || len(t.hist)-drop > historyChunks) {
		t.bytes -= len(t.hist[drop].Data)
		drop++
	}
	// 切片前移即可：底层数组用满后 append 会重新分配，只复制仍在用的部分
	t.hist = t.hist[drop:]
	for ch := range t.subs {
		select {
		case ch <- c:
		default:
		}
	}
}

// subscribe 返回当前历史与之后的实时数据，两者之间不会漏也不会重。
func (t *tap) subscribe() ([]Chunk, chan Chunk) {
	ch := make(chan Chunk, subscriberBuffer)
	t.mu.Lock()
	defer t.mu.Unlock()
	hist := append([]Chunk(nil), t.hist...)
	t.subs[ch] = struct{}{}
	return hist, ch
}

func (t *tap) unsubscribe(ch chan Chunk) {
	t.mu.Lock()
	delete(t.subs, ch)
	t.mu.Unlock()
}

func (t *tap) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.subs)
}
