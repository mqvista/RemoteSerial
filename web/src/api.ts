// 与 core 的 REST API 对接。路径清单见方案文档 4.3 节。

export interface TailscaleStatus {
  state: string
  hostname: string
  ip?: string
  dnsName?: string
}

export interface BridgeStatus {
  id: string
  name: string
  port: string
  tcpPort: number
  state: string
  client?: string
  bytesIn: number
  bytesOut: number
  lastError?: string
}

export interface Status {
  version: string
  commit: string
  mode: string
  uptimeSec: number
  dataDir: string
  listen: string
  source: string
  tailscale: TailscaleStatus
  bridges: BridgeStatus[]
}

export interface LogEntry {
  time: string
  level: string
  msg: string
  attrs?: string
}

export async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url, { cache: 'no-store' })
  if (!r.ok) throw new Error(`${r.status} ${r.statusText}`)
  return (await r.json()) as T
}

export const api = {
  status: () => getJSON<Status>('/api/status'),
  logs: (n = 500, level = 'debug') =>
    getJSON<{ entries: LogEntry[] }>(`/api/logs?n=${n}&level=${encodeURIComponent(level)}`),
  settings: () => getJSON<unknown>('/api/settings'),
}

export function formatUptime(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = Math.floor(sec % 60)
  if (d > 0) return `${d} 天 ${h} 时 ${m} 分`
  if (h > 0) return `${h} 时 ${m} 分 ${s} 秒`
  return `${m} 分 ${s} 秒`
}
