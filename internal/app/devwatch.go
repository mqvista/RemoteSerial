package app

// 串口设备插拔：每 2 秒比较一次串口列表，插上或拔掉时写日志，方便排查串口问题。

import (
	"context"
	"log/slog"
	"time"

	"rsagent/internal/bridge"
	"rsagent/internal/i18n"
)

type portEvent struct {
	added bool
	port  bridge.PortInfo
}

func diffPorts(old, cur map[string]bridge.PortInfo) []portEvent {
	var ev []portEvent
	for name, p := range cur {
		if _, ok := old[name]; !ok {
			ev = append(ev, portEvent{added: true, port: p})
		}
	}
	for name, p := range old {
		if _, ok := cur[name]; !ok {
			ev = append(ev, portEvent{added: false, port: p})
		}
	}
	return ev
}

func listPorts() (map[string]bridge.PortInfo, error) {
	ports, err := bridge.ListPorts()
	if err != nil {
		return nil, err
	}
	m := make(map[string]bridge.PortInfo, len(ports))
	for _, p := range ports {
		m[p.Name] = p
	}
	return m, nil
}

func devWatchLoop(ctx context.Context, log *slog.Logger) {
	prev, _ := listPorts() // 启动时只记录现状，不提示
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur, err := listPorts()
		if err != nil {
			continue
		}
		for _, e := range diffPorts(prev, cur) {
			if e.added {
				log.Info(i18n.T("串口设备已插入"), "port", e.port.Name, "chip", e.port.Chip, "usb", e.port.VID+":"+e.port.PID)
			} else {
				log.Info(i18n.T("串口设备已拔掉"), "port", e.port.Name)
			}
		}
		prev = cur
	}
}
