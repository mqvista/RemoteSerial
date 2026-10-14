package bridge

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"

	"rsagent/internal/config"
)

// Manager 维护全部桥接实例，并根据节点状态启停它们。
type Manager struct {
	log      *slog.Logger
	whois    WhoIsFunc
	onChange func()

	mu      sync.Mutex
	listen  ListenFunc
	bridges map[string]*Bridge
	order   []string
}

// NewManager 创建管理器。onChange 在任何桥接状态变化时被调用。
func NewManager(log *slog.Logger, whois WhoIsFunc, onChange func()) *Manager {
	return &Manager{
		log:      log,
		whois:    whois,
		onChange: onChange,
		bridges:  map[string]*Bridge{},
	}
}

// SetListener 在节点可监听时调用，启动所有启用的桥接。
func (m *Manager) SetListener(f ListenFunc) {
	m.mu.Lock()
	m.listen = f
	list := m.enabledLocked()
	m.mu.Unlock()
	for _, b := range list {
		b.start(f)
	}
}

// ClearListener 在节点停止时调用，停止所有桥接并标记为等待网络。
func (m *Manager) ClearListener() {
	m.mu.Lock()
	m.listen = nil
	all := m.allLocked()
	m.mu.Unlock()
	for _, b := range all {
		if b.cfg.Enabled {
			b.stop(StateWaitingNetwork)
		} else {
			b.stop(StateDisabled)
		}
	}
}

// Apply 用新的配置列表对齐运行中的桥接：变更的重建，删除的停止，新增的启动。
func (m *Manager) Apply(cfgs []config.Bridge) {
	m.mu.Lock()
	listen := m.listen
	seen := map[string]bool{}
	var toStop []*Bridge
	var toStart []*Bridge
	newOrder := make([]string, 0, len(cfgs))
	for _, c := range cfgs {
		seen[c.ID] = true
		newOrder = append(newOrder, c.ID)
		old, ok := m.bridges[c.ID]
		if ok && reflect.DeepEqual(old.cfg, c) {
			continue
		}
		if ok {
			toStop = append(toStop, old)
		}
		nb := newBridge(c, m.log, m.whois, m.onChange)
		m.bridges[c.ID] = nb
		if c.Enabled && listen != nil {
			toStart = append(toStart, nb)
		}
	}
	for id, b := range m.bridges {
		if !seen[id] {
			toStop = append(toStop, b)
			delete(m.bridges, id)
		}
	}
	m.order = newOrder
	m.mu.Unlock()

	for _, b := range toStop {
		b.stop(StateDisabled)
	}
	for _, b := range toStart {
		b.start(listen)
	}
	if m.onChange != nil {
		m.onChange()
	}
}

// SetEnabled 启用或停用一个桥接。调用方负责把 Enabled 持久化到配置。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	b, ok := m.bridges[id]
	listen := m.listen
	if ok {
		b.mu.Lock()
		b.cfg.Enabled = enabled
		b.mu.Unlock()
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("桥接 %q 不存在", id)
	}
	if !enabled {
		b.stop(StateDisabled)
		return nil
	}
	if listen == nil {
		b.stop(StateWaitingNetwork)
		return errors.New("Tailscale 节点未运行，桥接将在节点上线后自动启动")
	}
	b.start(listen)
	return nil
}

// Kick 断开指定桥接的客户端。
func (m *Manager) Kick(id string) error {
	m.mu.Lock()
	b, ok := m.bridges[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("桥接 %q 不存在", id)
	}
	if !b.kick() {
		return errors.New("当前没有客户端")
	}
	return nil
}

// Get 返回桥接实例。
func (m *Manager) Get(id string) (*Bridge, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bridges[id]
	return b, ok
}

// Status 返回全部桥接状态，按配置顺序。
func (m *Manager) Status() []Status {
	m.mu.Lock()
	list := m.allLocked()
	m.mu.Unlock()
	out := make([]Status, 0, len(list))
	for _, b := range list {
		out = append(out, b.Status())
	}
	return out
}

// UsedPorts 返回被桥接占用的串口名到桥接 id 的映射。
func (m *Manager) UsedPorts() map[string]string {
	m.mu.Lock()
	list := m.allLocked()
	m.mu.Unlock()
	out := map[string]string{}
	for _, b := range list {
		st := b.Status()
		if st.DeviceOpen && st.ResolvedPort != "" {
			out[st.ResolvedPort] = st.ID
		}
	}
	return out
}

// StopAll 停止全部桥接，进程退出时调用。
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := m.allLocked()
	m.mu.Unlock()
	for _, b := range all {
		b.stop(StateDisabled)
	}
}

func (m *Manager) allLocked() []*Bridge {
	out := make([]*Bridge, 0, len(m.order))
	for _, id := range m.order {
		if b, ok := m.bridges[id]; ok {
			out = append(out, b)
		}
	}
	return out
}

func (m *Manager) enabledLocked() []*Bridge {
	var out []*Bridge
	for _, b := range m.allLocked() {
		if b.cfg.Enabled {
			out = append(out, b)
		}
	}
	return out
}
