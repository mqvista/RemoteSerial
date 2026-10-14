// 与 core 的 REST API 对接。路径清单见方案文档 4.3 节。

export interface TailscaleSnapshot {
  state: string
  hostname: string
  controlURL: string
  hasState: boolean
  ip?: string
  ipv6?: string
  dnsName?: string
  tailnet?: string
  loginName?: string
  tags?: string[]
  keyExpiry?: string
  message?: string
  since: string
}

export interface Identity {
  loginName: string
  displayName: string
  node: string
  tags?: string[]
}

export interface Peer {
  name: string
  hostName: string
  ip: string
  os: string
  online: boolean
  direct: boolean
  curAddr?: string
  relay?: string
  lastHandshake: string
  rxBytes: number
  txBytes: number
}

export interface BridgeStatus {
  id: string
  name: string
  port: string
  resolvedPort: string
  tcpPort: number
  mode: string
  baud: number
  enabled: boolean
  state: string
  deviceOpen: boolean
  client?: string
  clientSince?: string
  fromDevice: number
  toDevice: number
  lastError?: string
}

export interface PortMatch {
  vid: string
  pid: string
  serial: string
}

export interface BridgeConfig {
  id: string
  name: string
  port: string
  match?: PortMatch | null
  baud: number
  dataBits: number
  parity: string
  stopBits: number
  mode: string
  tcpPort: number
  clientPolicy: string
  idleTimeoutSec: number
  enabled: boolean
}

export interface BridgeView {
  config: BridgeConfig
  status?: BridgeStatus
}

export interface SerialPort {
  name: string
  isUSB: boolean
  vid?: string
  pid?: string
  serialNumber?: string
  chip?: string
  usedBy?: string
}

export interface Status {
  version: string
  commit: string
  mode: string
  uptimeSec: number
  dataDir: string
  listen: string
  source?: string
  identity?: Identity
  accessWarning: boolean
  tailscale: TailscaleSnapshot
  webPort: number
  bridges: BridgeStatus[]
}

export interface LogEntry {
  time: string
  level: string
  msg: string
  attrs?: string
}

export interface RouteStatus {
  advertised: string[]
  approved: string[]
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const r = await fetch(url, { cache: 'no-store', ...init })
  if (!r.ok) {
    let msg = `${r.status} ${r.statusText}`
    try {
      const body = await r.json()
      if (body?.error) msg = body.error
    } catch {
      // 非 JSON 错误体
    }
    throw new Error(msg)
  }
  return (await r.json()) as T
}

function json(method: string, body: unknown): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

export const api = {
  status: () => request<Status>('/api/status'),
  logs: (n = 500, level = 'debug') => request<{ entries: LogEntry[] }>(`/api/logs?n=${n}&level=${encodeURIComponent(level)}`),
  settings: () => request<unknown>('/api/settings'),

  tailscaleStatus: () => request<TailscaleSnapshot>('/api/tailscale/status'),
  tailscaleLogin: (body: { authKey: string; hostname: string; controlURL: string }) =>
    request<TailscaleSnapshot>('/api/tailscale/login', json('POST', body)),
  tailscaleStart: () => request<TailscaleSnapshot>('/api/tailscale/start', { method: 'POST' }),
  tailscaleStop: () => request<TailscaleSnapshot>('/api/tailscale/stop', { method: 'POST' }),
  tailscaleLogout: () => request<TailscaleSnapshot>('/api/tailscale/logout', { method: 'POST' }),
  tailscalePeers: () => request<{ peers: Peer[] }>('/api/tailscale/peers'),
  routes: () => request<RouteStatus>('/api/tailscale/routes'),
  setRoutes: (routes: string[]) => request<RouteStatus>('/api/tailscale/routes', json('PUT', { routes })),

  serialPorts: () => request<{ ports: SerialPort[] }>('/api/serial/ports'),
  bridges: () => request<{ bridges: BridgeView[] }>('/api/bridges'),
  saveBridges: (bridges: BridgeConfig[]) => request<{ bridges: BridgeView[] }>('/api/bridges', json('PUT', { bridges })),
  bridgeAction: (id: string, action: 'start' | 'stop' | 'kick') =>
    request<{ bridges: BridgeView[]; warning?: string }>(`/api/bridges/${encodeURIComponent(id)}/${action}`, { method: 'POST' }),
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

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export const BAUD_RATES = [1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600, 1500000, 2000000]
