// 网页终端的流量记录与“监看”视图渲染。
//
// 服务端二进制帧由若干记录拼成：方向(1) + Unix 微秒(8，大端) + 长度(4，大端) + 数据。
// 监看视图把记录排成带时间戳与 RX/TX 标记的行，输出 xterm 能直接写入的 ANSI 文本。

export const DIR_RX = 0
export const DIR_TX = 1 // TCP 客户端写入
export const DIR_TX_WEB = 2 // 网页终端写入

export interface TrafficRecord {
  dir: number
  time: number // 毫秒，含小数
  data: Uint8Array
}

export function parseFrame(buf: ArrayBuffer): TrafficRecord[] {
  const view = new DataView(buf)
  const out: TrafficRecord[] = []
  let off = 0
  while (off + 13 <= buf.byteLength) {
    const dir = view.getUint8(off)
    const us = view.getBigUint64(off + 1)
    const len = view.getUint32(off + 9)
    off += 13
    if (off + len > buf.byteLength) break
    out.push({ dir, time: Number(us) / 1000, data: new Uint8Array(buf, off, len) })
    off += len
  }
  return out
}

export interface MonitorOptions {
  showRx: boolean
  showTx: boolean
  timestamp: boolean
  hex: boolean
  gapMs: number // 同方向闲置超过这么久另起一行，0 表示不按闲置断行
}

const ESC = '\x1b['
const DIM = ESC + '2m'
const UNDIM = ESC + '22m'
const RESET = ESC + '0m'
const TAG: Record<number, string> = {
  [DIR_RX]: ESC + '32mRX' + RESET, // 绿
  [DIR_TX]: ESC + '33mTX' + RESET, // 黄
  [DIR_TX_WEB]: ESC + '36mTX' + RESET, // 青：本页送出
}
const HEX_PER_LINE = 16

function pad(n: number, w = 2) {
  return String(n).padStart(w, '0')
}

export function formatClock(ms: number): string {
  const d = new Date(ms)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
}

export function isTx(dir: number) {
  return dir === DIR_TX || dir === DIR_TX_WEB
}

// MonitorRenderer 保存跨记录的行状态：当前行属于哪个方向、最后一次数据的时间、
// 十六进制行已写了几个字节、刚以 CR 结束的行后面紧跟的 LF 要吞掉。
export class MonitorRenderer {
  private open = false
  private dir = -1
  private last = 0
  private col = 0
  private skipLF = false
  private decoders = new Map<number, TextDecoder>()

  private o: MonitorOptions

  constructor(o: MonitorOptions) {
    this.o = o
  }

  visible(dir: number) {
    return dir === DIR_RX ? this.o.showRx : this.o.showTx
  }

  private prefix(time: number, cont = false): string {
    let s = ''
    if (this.o.timestamp) s += cont ? ' '.repeat(15) : `${DIM}[${formatClock(time)}]${UNDIM} `
    s += cont ? '   ' : TAG[this.dir] + ' '
    return s
  }

  private endLine(): string {
    if (!this.open) return ''
    this.open = false
    this.col = 0
    return '\r\n'
  }

  private decoder(dir: number) {
    let d = this.decoders.get(dir)
    if (!d) {
      d = new TextDecoder('utf-8', { fatal: false })
      this.decoders.set(dir, d)
    }
    return d
  }

  render(r: TrafficRecord): string {
    if (!this.visible(r.dir) || r.data.length === 0) return ''
    let out = ''
    // 方向变化（同为 TX 但来源不同也算）或闲置超时就另起一行
    if (this.open && (r.dir !== this.dir || (this.o.gapMs > 0 && r.time - this.last > this.o.gapMs))) {
      out += this.endLine()
    }
    if (r.dir !== this.dir) this.skipLF = false
    this.dir = r.dir
    this.last = r.time
    const start = () => {
      if (!this.open) {
        out += this.prefix(r.time)
        this.open = true
        this.col = 0
      }
    }

    if (this.o.hex) {
      for (const b of r.data) {
        if (this.open && this.col >= HEX_PER_LINE) {
          out += '\r\n' + this.prefix(r.time, true)
          this.col = 0
        }
        start()
        out += (this.col ? ' ' : '') + b.toString(16).padStart(2, '0').toUpperCase()
        this.col++
      }
      return out
    }

    const text = this.decoder(r.dir).decode(r.data, { stream: true })
    for (const ch of text) {
      if (ch === '\n' && this.skipLF) {
        this.skipLF = false
        continue
      }
      this.skipLF = false
      if (ch === '\r' || ch === '\n') {
        // 空行也保留一行，保持与设备输出一致
        start()
        out += this.endLine()
        this.skipLF = ch === '\r'
        continue
      }
      start()
      const code = ch.codePointAt(0)!
      if (ch === '\t') out += ch
      else if (code < 0x20 || code === 0x7f) out += `${DIM}<${code.toString(16).padStart(2, '0').toUpperCase()}>${UNDIM}`
      else out += ch
    }
    return out
  }
}

// parseHex 接受 "01 03 0A"、"01030A"、"0x01,0x03" 等写法。
export function parseHex(s: string): Uint8Array<ArrayBuffer> | null {
  const clean = s.replace(/0x/gi, '').replace(/[\s,;:-]/g, '')
  if (clean.length === 0 || clean.length % 2 !== 0 || /[^0-9a-f]/i.test(clean)) return null
  const out = new Uint8Array(clean.length / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16)
  return out
}
