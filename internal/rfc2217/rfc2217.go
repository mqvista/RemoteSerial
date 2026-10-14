// Package rfc2217 实现 RFC 2217（Telnet COM-PORT-OPTION）服务端。
//
// 它把一条 TCP 连接包装成 io.ReadWriteCloser：Read 返回去掉 telnet 协商后的客户端数据，
// Write 把设备数据做 IAC 转义后发给客户端。客户端的 SET-BAUDRATE、SET-CONTROL 等
// 命令通过 Port 接口作用到真实串口，并按协议回应。
//
// 互操作对象：pyserial 的 rfc2217://、esptool、hub4com 的 com2tcp-rfc2217。
package rfc2217

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Telnet 常量。
const (
	iac  = 255
	dont = 254
	do   = 253
	wont = 252
	will = 251
	sb   = 250
	se   = 240
	nop  = 241

	optBinary  = 0
	optEcho    = 1
	optSGA     = 3
	optComPort = 44
)

// COM-PORT-OPTION 子命令（客户端 → 服务端），服务端回应用 +100。
const (
	cmdSignature       = 0
	cmdSetBaudrate     = 1
	cmdSetDatasize     = 2
	cmdSetParity       = 3
	cmdSetStopsize     = 4
	cmdSetControl      = 5
	cmdNotifyLinestate = 6
	cmdNotifyModem     = 7
	cmdFlowSuspend     = 8
	cmdFlowResume      = 9
	cmdSetLinestateMsk = 10
	cmdSetModemMask    = 11
	cmdPurgeData       = 12
	respOffset         = 100
)

// SET-CONTROL 取值。
const (
	ctlFlowQuery   = 0
	ctlFlowNone    = 1
	ctlFlowXonXoff = 2
	ctlFlowRtsCts  = 3
	ctlBreakQuery  = 4
	ctlBreakOn     = 5
	ctlBreakOff    = 6
	ctlDTRQuery    = 7
	ctlDTROn       = 8
	ctlDTROff      = 9
	ctlRTSQuery    = 10
	ctlRTSOn       = 11
	ctlRTSOff      = 12
)

// Mode 是串口参数。Parity 取 N/O/E/M/S。
type Mode struct {
	Baud     int
	DataBits int
	Parity   byte
	StopBits float64
}

// String 形如 115200 8N1。
func (m Mode) String() string {
	stop := "1"
	switch m.StopBits {
	case 1.5:
		stop = "1.5"
	case 2:
		stop = "2"
	}
	return fmt.Sprintf("%d %d%c%s", m.Baud, m.DataBits, m.Parity, stop)
}

// ModemStatus 是输入控制线状态。
type ModemStatus struct {
	CTS, DSR, RI, DCD bool
}

// Port 是服务端控制串口所需的能力，由桥接实现。设备不在时返回错误即可。
type Port interface {
	GetMode() Mode
	SetMode(Mode) error
	SetDTR(bool) error
	SetRTS(bool) error
	DTR() bool
	RTS() bool
	Break(time.Duration) error
	Purge(rx, tx bool) error
	ModemStatus() (ModemStatus, error)
}

// Server 是一条 RFC2217 会话。
type Server struct {
	conn net.Conn
	port Port
	log  *slog.Logger
	br   *bufio.Reader

	wmu       sync.Mutex
	suspended bool
	resumeCh  chan struct{}

	mu        sync.Mutex
	sentWill  map[byte]bool
	sentDo    map[byte]bool
	modemMask byte
	lastModem byte
	sentModem bool
	closeOnce sync.Once
	closed    chan struct{}

	// 解析状态
	st     parseState
	sbBuf  []byte
	sbIac  bool
	pend   []byte // 已解码待返回给 Read 的数据
	pendAt int

	// OnChange 在客户端修改串口参数或控制线后被调用，可为空。
	OnChange func()
}

type parseState int

const (
	stData parseState = iota
	stIAC
	stWill
	stWont
	stDo
	stDont
	stSB
)

// NewServer 包装连接。调用 Start 发起协商。
func NewServer(conn net.Conn, port Port, log *slog.Logger) *Server {
	return &Server{
		conn:      conn,
		port:      port,
		log:       log,
		br:        bufio.NewReaderSize(conn, 8192),
		resumeCh:  make(chan struct{}),
		sentWill:  map[byte]bool{},
		sentDo:    map[byte]bool{},
		modemMask: 0xFF,
		closed:    make(chan struct{}),
	}
}

// Start 发送初始协商并启动调制解调器状态轮询。
func (s *Server) Start() error {
	s.mu.Lock()
	initial := []byte{
		iac, will, optComPort,
		iac, will, optBinary,
		iac, do, optBinary,
		iac, will, optSGA,
		iac, do, optSGA,
	}
	s.sentWill[optComPort], s.sentWill[optBinary], s.sentWill[optSGA] = true, true, true
	s.sentDo[optBinary], s.sentDo[optSGA] = true, true
	s.mu.Unlock()
	if _, err := s.conn.Write(initial); err != nil {
		return err
	}
	go s.pollModem()
	return nil
}

// Close 关闭连接。
func (s *Server) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return s.conn.Close()
}

// Conn 返回底层连接，用于设置超时等。
func (s *Server) Conn() net.Conn { return s.conn }

// Read 返回解码后的客户端数据。
func (s *Server) Read(p []byte) (int, error) {
	for {
		if s.pendAt < len(s.pend) {
			n := copy(p, s.pend[s.pendAt:])
			s.pendAt += n
			if s.pendAt >= len(s.pend) {
				s.pend = s.pend[:0]
				s.pendAt = 0
			}
			return n, nil
		}
		// 批量读取一段原始字节再解析，避免逐字节系统调用。
		buf := make([]byte, 4096)
		n, err := s.br.Read(buf)
		if n > 0 {
			if perr := s.feed(buf[:n]); perr != nil {
				return 0, perr
			}
		}
		if err != nil {
			if s.pendAt < len(s.pend) {
				continue
			}
			return 0, err
		}
	}
}

// feed 把原始字节过一遍状态机，数据进 pend，命令即时处理。
func (s *Server) feed(buf []byte) error {
	for _, c := range buf {
		switch s.st {
		case stData:
			if c == iac {
				s.st = stIAC
			} else {
				s.pend = append(s.pend, c)
			}
		case stIAC:
			switch c {
			case iac:
				s.pend = append(s.pend, iac)
				s.st = stData
			case will:
				s.st = stWill
			case wont:
				s.st = stWont
			case do:
				s.st = stDo
			case dont:
				s.st = stDont
			case sb:
				s.st = stSB
				s.sbBuf = s.sbBuf[:0]
				s.sbIac = false
			default:
				// NOP、GA 等无参数命令
				s.st = stData
			}
		case stWill, stWont, stDo, stDont:
			if err := s.handleOption(s.st, c); err != nil {
				return err
			}
			s.st = stData
		case stSB:
			if s.sbIac {
				s.sbIac = false
				if c == se {
					if err := s.handleSub(s.sbBuf); err != nil {
						return err
					}
					s.st = stData
					continue
				}
				if c == iac {
					s.sbBuf = append(s.sbBuf, iac)
					continue
				}
				// 非法序列，丢弃
				s.sbBuf = append(s.sbBuf, iac, c)
				continue
			}
			if c == iac {
				s.sbIac = true
				continue
			}
			s.sbBuf = append(s.sbBuf, c)
			if len(s.sbBuf) > 1024 {
				return errors.New("rfc2217: 子协商过长")
			}
		}
	}
	return nil
}

func (s *Server) handleOption(st parseState, opt byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch st {
	case stDo:
		switch opt {
		case optComPort, optBinary, optSGA:
			if !s.sentWill[opt] {
				s.sentWill[opt] = true
				return s.rawWrite([]byte{iac, will, opt})
			}
		default:
			return s.rawWrite([]byte{iac, wont, opt})
		}
	case stWill:
		switch opt {
		case optBinary, optSGA:
			if !s.sentDo[opt] {
				s.sentDo[opt] = true
				return s.rawWrite([]byte{iac, do, opt})
			}
		default:
			return s.rawWrite([]byte{iac, dont, opt})
		}
	case stDont:
		if opt == optComPort {
			s.log.Debug("rfc2217: 客户端拒绝 COM-PORT-OPTION")
		}
		delete(s.sentWill, opt)
	case stWont:
		delete(s.sentDo, opt)
	}
	return nil
}

// handleSub 处理 IAC SB ... IAC SE 子协商。
func (s *Server) handleSub(b []byte) error {
	if len(b) < 2 || b[0] != optComPort {
		return nil
	}
	cmd := b[1]
	data := b[2:]
	switch cmd {
	case cmdSignature:
		if len(data) == 0 {
			return s.reply(cmdSignature, []byte("rsagent"))
		}
		s.log.Debug("rfc2217: 客户端签名", "sig", string(data))
		return nil

	case cmdSetBaudrate:
		if len(data) < 4 {
			return nil
		}
		baud := int(binary.BigEndian.Uint32(data[:4]))
		if baud != 0 {
			m := s.port.GetMode()
			m.Baud = baud
			if err := s.port.SetMode(m); err != nil {
				s.log.Warn("rfc2217: 设置波特率失败", "baud", baud, "err", err)
			} else {
				s.changed()
			}
		}
		out := make([]byte, 4)
		binary.BigEndian.PutUint32(out, uint32(s.port.GetMode().Baud))
		return s.reply(cmdSetBaudrate, out)

	case cmdSetDatasize:
		if len(data) < 1 {
			return nil
		}
		if v := int(data[0]); v >= 5 && v <= 8 {
			m := s.port.GetMode()
			m.DataBits = v
			if err := s.port.SetMode(m); err != nil {
				s.log.Warn("rfc2217: 设置数据位失败", "bits", v, "err", err)
			} else {
				s.changed()
			}
		}
		return s.reply(cmdSetDatasize, []byte{byte(s.port.GetMode().DataBits)})

	case cmdSetParity:
		if len(data) < 1 {
			return nil
		}
		if p, ok := parityFromWire(data[0]); ok {
			m := s.port.GetMode()
			m.Parity = p
			if err := s.port.SetMode(m); err != nil {
				s.log.Warn("rfc2217: 设置校验位失败", "parity", string(p), "err", err)
			} else {
				s.changed()
			}
		}
		return s.reply(cmdSetParity, []byte{parityToWire(s.port.GetMode().Parity)})

	case cmdSetStopsize:
		if len(data) < 1 {
			return nil
		}
		if sb, ok := stopFromWire(data[0]); ok {
			m := s.port.GetMode()
			m.StopBits = sb
			if err := s.port.SetMode(m); err != nil {
				s.log.Warn("rfc2217: 设置停止位失败", "stop", sb, "err", err)
			} else {
				s.changed()
			}
		}
		return s.reply(cmdSetStopsize, []byte{stopToWire(s.port.GetMode().StopBits)})

	case cmdSetControl:
		if len(data) < 1 {
			return nil
		}
		return s.reply(cmdSetControl, []byte{s.control(data[0])})

	case cmdNotifyLinestate:
		// 客户端主动查询线路状态（pyserial 的 poll_modem 选项），当前不跟踪，报告无错误。
		return s.reply(cmdNotifyLinestate, []byte{0})

	case cmdNotifyModem:
		v, _ := s.modemByte()
		s.mu.Lock()
		mask := s.modemMask
		s.lastModem = v
		s.sentModem = true
		s.mu.Unlock()
		return s.reply(cmdNotifyModem, []byte{v & mask})

	case cmdFlowSuspend:
		s.setSuspended(true)
		return s.reply(cmdFlowSuspend, nil)
	case cmdFlowResume:
		s.setSuspended(false)
		return s.reply(cmdFlowResume, nil)

	case cmdSetLinestateMsk:
		if len(data) < 1 {
			return nil
		}
		return s.reply(cmdSetLinestateMsk, []byte{data[0]})

	case cmdSetModemMask:
		if len(data) < 1 {
			return nil
		}
		s.mu.Lock()
		s.modemMask = data[0]
		s.mu.Unlock()
		return s.reply(cmdSetModemMask, []byte{data[0]})

	case cmdPurgeData:
		if len(data) < 1 {
			return nil
		}
		rx := data[0] == 1 || data[0] == 3
		tx := data[0] == 2 || data[0] == 3
		if err := s.port.Purge(rx, tx); err != nil {
			s.log.Debug("rfc2217: 清空缓冲失败", "err", err)
		}
		return s.reply(cmdPurgeData, []byte{data[0]})
	}
	return nil
}

// control 执行 SET-CONTROL，返回回应值。
func (s *Server) control(v byte) byte {
	switch v {
	case ctlFlowQuery, ctlFlowNone, ctlFlowXonXoff, ctlFlowRtsCts:
		// 当前不支持流控，一律报告无流控。
		return ctlFlowNone
	case ctlBreakQuery:
		return ctlBreakOff
	case ctlBreakOn:
		if err := s.port.Break(250 * time.Millisecond); err != nil {
			s.log.Debug("rfc2217: break 失败", "err", err)
		}
		return ctlBreakOn
	case ctlBreakOff:
		return ctlBreakOff
	case ctlDTRQuery:
		return dtrWire(s.port.DTR())
	case ctlDTROn, ctlDTROff:
		on := v == ctlDTROn
		if err := s.port.SetDTR(on); err != nil {
			s.log.Debug("rfc2217: 设置 DTR 失败", "on", on, "err", err)
		} else {
			s.changed()
		}
		return dtrWire(s.port.DTR())
	case ctlRTSQuery:
		return rtsWire(s.port.RTS())
	case ctlRTSOn, ctlRTSOff:
		on := v == ctlRTSOn
		if err := s.port.SetRTS(on); err != nil {
			s.log.Debug("rfc2217: 设置 RTS 失败", "on", on, "err", err)
		} else {
			s.changed()
		}
		return rtsWire(s.port.RTS())
	}
	return v
}

func dtrWire(on bool) byte {
	if on {
		return ctlDTROn
	}
	return ctlDTROff
}

func rtsWire(on bool) byte {
	if on {
		return ctlRTSOn
	}
	return ctlRTSOff
}

func parityFromWire(v byte) (byte, bool) {
	switch v {
	case 1:
		return 'N', true
	case 2:
		return 'O', true
	case 3:
		return 'E', true
	case 4:
		return 'M', true
	case 5:
		return 'S', true
	}
	return 0, false
}

func parityToWire(p byte) byte {
	switch p {
	case 'O':
		return 2
	case 'E':
		return 3
	case 'M':
		return 4
	case 'S':
		return 5
	}
	return 1
}

func stopFromWire(v byte) (float64, bool) {
	switch v {
	case 1:
		return 1, true
	case 2:
		return 2, true
	case 3:
		return 1.5, true
	}
	return 0, false
}

func stopToWire(sb float64) byte {
	switch sb {
	case 2:
		return 2
	case 1.5:
		return 3
	}
	return 1
}

func (s *Server) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// reply 发送 COM-PORT-OPTION 回应。
func (s *Server) reply(cmd byte, data []byte) error {
	msg := []byte{iac, sb, optComPort, cmd + respOffset}
	for _, c := range data {
		if c == iac {
			msg = append(msg, iac, iac)
		} else {
			msg = append(msg, c)
		}
	}
	msg = append(msg, iac, se)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rawWrite(msg)
}

// rawWrite 直接写连接，调用方持有 s.mu 或在 Write 路径持有 wmu。
func (s *Server) rawWrite(b []byte) error {
	_, err := s.conn.Write(b)
	return err
}

// Write 把设备数据转义后发给客户端。客户端 FLOWCONTROL-SUSPEND 期间阻塞。
func (s *Server) Write(p []byte) (int, error) {
	if err := s.waitResume(); err != nil {
		return 0, err
	}
	out := make([]byte, 0, len(p)+8)
	for _, c := range p {
		if c == iac {
			out = append(out, iac, iac)
		} else {
			out = append(out, c)
		}
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.conn.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) setSuspended(v bool) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.suspended == v {
		return
	}
	s.suspended = v
	if !v {
		close(s.resumeCh)
		s.resumeCh = make(chan struct{})
	}
}

func (s *Server) waitResume() error {
	for {
		s.wmu.Lock()
		if !s.suspended {
			s.wmu.Unlock()
			return nil
		}
		ch := s.resumeCh
		s.wmu.Unlock()
		select {
		case <-ch:
		case <-s.closed:
			return io.ErrClosedPipe
		case <-time.After(10 * time.Second):
			// 客户端长时间不恢复，放弃等待，避免串口读循环被卡死。
			s.setSuspended(false)
			return nil
		}
	}
}

// modemByte 把控制线状态编码为 RFC2217 的 modemstate 字节。
func (s *Server) modemByte() (byte, error) {
	ms, err := s.port.ModemStatus()
	if err != nil {
		return 0, err
	}
	var v byte
	if ms.CTS {
		v |= 0x10
	}
	if ms.DSR {
		v |= 0x20
	}
	if ms.RI {
		v |= 0x40
	}
	if ms.DCD {
		v |= 0x80
	}
	return v, nil
}

// pollModem 定期读取控制线：首次读到后立即通知一次（pyserial 等客户端依赖这一条），
// 之后只在按掩码过滤后有变化时通知。
func (s *Server) pollModem() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	first := true
	for {
		if !first {
			select {
			case <-s.closed:
				return
			case <-t.C:
			}
		}
		first = false
		v, err := s.modemByte()
		if err != nil {
			continue
		}
		s.mu.Lock()
		mask := s.modemMask
		send := !s.sentModem || (v&mask) != (s.lastModem&mask)
		s.lastModem = v
		s.sentModem = true
		s.mu.Unlock()
		if send {
			_ = s.reply(cmdNotifyModem, []byte{v & mask})
		}
	}
}
