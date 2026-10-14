package bridge

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"

	"rsagent/internal/config"
)

// ErrDeviceNotFound 表示按 USB 标识没有匹配到任何串口。
var ErrDeviceNotFound = errors.New("没有匹配的 USB 串口")

// PortInfo 是枚举到的一个串口。
type PortInfo struct {
	Name         string `json:"name"`
	IsUSB        bool   `json:"isUSB"`
	VID          string `json:"vid,omitempty"`
	PID          string `json:"pid,omitempty"`
	SerialNumber string `json:"serialNumber,omitempty"`
	Chip         string `json:"chip,omitempty"`   // 按 VID/PID 猜测的芯片型号
	UsedBy       string `json:"usedBy,omitempty"` // 占用它的桥接 id
}

// 常见 USB 转串口芯片，用于界面显示。
var knownChips = map[string]string{
	"1A86:7523": "CH340",
	"1A86:55D4": "CH9102",
	"1A86:55D3": "CH343",
	"1A86:7522": "CH340K",
	"10C4:EA60": "CP210x",
	"10C4:EA70": "CP2105",
	"0403:6001": "FT232R",
	"0403:6010": "FT2232",
	"0403:6011": "FT4232",
	"0403:6014": "FT232H",
	"0403:6015": "FT231X",
	"067B:2303": "PL2303",
	"067B:2323": "PL2303",
	"067B:23A3": "PL2303GC",
	"303A:1001": "ESP32-S2/S3 USB-JTAG",
	"2341:0043": "Arduino Uno",
	"1366:0105": "J-Link CDC",
	"0483:5740": "STM32 VCP",
	"0483:374B": "ST-LINK VCP",
	"0483:374E": "ST-LINK V3 VCP",
	"0D28:0204": "DAPLink CDC",
}

// ListPorts 枚举串口。usedBy 由调用方按桥接占用情况填充。
func ListPorts() ([]PortInfo, error) {
	details, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	out := make([]PortInfo, 0, len(details))
	for _, d := range details {
		p := PortInfo{
			Name:         d.Name,
			IsUSB:        d.IsUSB,
			VID:          strings.ToUpper(d.VID),
			PID:          strings.ToUpper(d.PID),
			SerialNumber: d.SerialNumber,
		}
		if p.IsUSB {
			p.Chip = knownChips[p.VID+":"+p.PID]
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
	return out, nil
}

// naturalLess 让 COM2 排在 COM10 前面。
func naturalLess(a, b string) bool {
	an, bn := trailingNumber(a), trailingNumber(b)
	pa, pb := strings.TrimRight(a, "0123456789"), strings.TrimRight(b, "0123456789")
	if pa != pb {
		return pa < pb
	}
	return an < bn
}

func trailingNumber(s string) int {
	n := 0
	mult := 1
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		n += int(c-'0') * mult
		mult *= 10
	}
	return n
}

// resolvePort 根据配置找到实际串口名：优先按 USB 标识匹配，其次用配置的名字。
func resolvePort(cfg config.Bridge) (string, error) {
	if cfg.Match == nil {
		if cfg.Port == "" {
			return "", fmt.Errorf("未配置串口")
		}
		return cfg.Port, nil
	}
	ports, err := ListPorts()
	if err != nil {
		return "", fmt.Errorf("枚举串口: %w", err)
	}
	wantVID := strings.ToUpper(strings.TrimSpace(cfg.Match.VID))
	wantPID := strings.ToUpper(strings.TrimSpace(cfg.Match.PID))
	wantSN := strings.TrimSpace(cfg.Match.Serial)
	var candidates []string
	for _, p := range ports {
		if !p.IsUSB {
			continue
		}
		if wantVID != "" && p.VID != wantVID {
			continue
		}
		if wantPID != "" && p.PID != wantPID {
			continue
		}
		if wantSN != "" && p.SerialNumber != wantSN {
			continue
		}
		candidates = append(candidates, p.Name)
	}
	switch len(candidates) {
	case 0:
		if cfg.Port != "" {
			return cfg.Port, nil
		}
		return "", ErrDeviceNotFound
	case 1:
		return candidates[0], nil
	default:
		// 多个匹配时优先配置里记录的名字，否则取第一个。
		for _, c := range candidates {
			if c == cfg.Port {
				return c, nil
			}
		}
		return candidates[0], nil
	}
}

// serialMode 把配置转换为串口库的参数。
func serialMode(cfg config.Bridge) (*serial.Mode, error) {
	m := &serial.Mode{BaudRate: cfg.Baud, DataBits: cfg.DataBits}
	switch strings.ToUpper(cfg.Parity) {
	case "N", "":
		m.Parity = serial.NoParity
	case "E":
		m.Parity = serial.EvenParity
	case "O":
		m.Parity = serial.OddParity
	case "M":
		m.Parity = serial.MarkParity
	case "S":
		m.Parity = serial.SpaceParity
	default:
		return nil, fmt.Errorf("校验位非法: %q", cfg.Parity)
	}
	switch cfg.StopBits {
	case 0, 1:
		m.StopBits = serial.OneStopBit
	case 1.5:
		m.StopBits = serial.OnePointFiveStopBits
	case 2:
		m.StopBits = serial.TwoStopBits
	default:
		return nil, fmt.Errorf("停止位非法: %v", cfg.StopBits)
	}
	return m, nil
}

// isNotFound 判断是否是串口不存在（设备被拔出或尚未接入）。
func isNotFound(err error) bool {
	if errors.Is(err, ErrDeviceNotFound) {
		return true
	}
	var pe *serial.PortError
	if errors.As(err, &pe) {
		return pe.Code() == serial.PortNotFound
	}
	return false
}

func isBusy(err error) bool {
	var pe *serial.PortError
	if errors.As(err, &pe) {
		return pe.Code() == serial.PortBusy
	}
	return false
}
