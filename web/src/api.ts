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
  homeRelay?: string
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
  localPort?: number
  mode: string
  baud: number
  active: string
  paramsBy: string
  dtr: boolean
  rts: boolean
  enabled: boolean
  state: string
  deviceOpen: boolean
  viewers: number
  capture: CaptureStatus
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
  localPort?: number
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
  update: { available: boolean; latest?: string; downloaded?: string; error?: string }
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

export interface PingResult {
  type: string
  ok: boolean
  latencyMs: number
  endpoint?: string
  derp?: string
  peerRelay?: string
  nodeName?: string
  err?: string
}

export interface RegionLatency {
  id: number
  code: string
  name: string
  latencyMs: number
  preferred: boolean
}

export interface NetcheckReport {
  udp: boolean
  ipv4: boolean
  ipv6: boolean
  hardNAT?: boolean
  upnp?: boolean
  pmp?: boolean
  pcp?: boolean
  captivePortal?: boolean
  globalV4?: string
  globalV6?: string
  preferredDERP?: string
  regions: RegionLatency[]
  tookMs: number
}

export interface CaptureStatus {
  active: boolean
  base?: string
  started?: string
  rxBytes: number
  txBytes: number
}

export interface CaptureFile {
  name: string
  size: number
  modTime: string
}

export interface InterfaceInfo {
  name: string
  ip: string
  cidr: string
  hidden: boolean
}

export interface Config {
  version: number
  listen: string
  logLevel: string
  tailscale: { hostname: string; controlURL: string; udpPort: number; webPort: number; advertiseRoutes: string[] }
  access: { allowedLogins: string[] }
  update: { url: string; autoCheck: boolean }
  bridges: BridgeConfig[]
}

export interface UpdateLatest {
  version: string
  notes?: string
  publishedAt?: string
  url: string
  sha256: string
  size: number
}

export interface UpdatePending {
  version: string
  file: string
  sha256: string
  size: number
  downloadedAt: string
}

export interface UpdateStatus {
  current: string
  platform: string
  url: string
  autoCheck: boolean
  checking: boolean
  lastCheck?: string
  available: boolean
  latest?: UpdateLatest
  downloading: boolean
  received: number
  total: number
  downloaded?: UpdatePending
  error?: string
}

export interface SettingsUpdate {
  logLevel?: string
  allowedLogins?: string[]
  listen?: string
  webPort?: number
  udpPort?: number
  hostname?: string
  controlURL?: string
  updateURL?: string
  updateAutoCheck?: boolean
}

export interface SettingsResponse {
  config: Config
  restartRequired: boolean
  applied: string[]
  pending: string[]
}

export interface DeployStatus {
  os: string
  elevated: boolean
  exePath: string
  dataDir: string
  mode: string
  hasShell: boolean
  task: { supported: boolean; installed: boolean; runLevel?: string; command?: string }
  service: { supported: boolean; installed: boolean; running: boolean; binary?: string; detail?: string }
  firewall: { supported: boolean; installed: boolean; udpPort: number }
  webview2: { supported: boolean; installed: boolean; version?: string }
  autoLogon: { supported: boolean; enabled: boolean; user?: string }
}

export interface DeployResult {
  action: string
  ok: boolean
  message: string
  time: string
}

export type DeployAction =
  | 'task-install'
  | 'task-remove'
  | 'service-install'
  | 'service-remove'
  | 'service-start'
  | 'service-stop'
  | 'service-restart'
  | 'firewall-add'
  | 'firewall-remove'
  | 'webview2-install'

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
  settings: () => request<Config>('/api/settings'),
  saveSettings: (u: SettingsUpdate) => request<SettingsResponse>('/api/settings', json('PUT', u)),
  interfaces: () => request<{ interfaces: InterfaceInfo[] }>('/api/network/interfaces'),
  deployStatus: () => request<DeployStatus>('/api/deploy/status'),
  deployAction: async (action: DeployAction) => {
    const r = await fetch(`/api/deploy/${action}`, { method: 'POST', cache: 'no-store' })
    const body = (await r.json()) as { result: DeployResult; status: DeployStatus; error?: string }
    if (!r.ok && !body.result) throw new Error(body.error ?? `${r.status}`)
    return body
  },

  tailscaleStatus: () => request<TailscaleSnapshot>('/api/tailscale/status'),
  tailscaleLogin: (body: { authKey: string; hostname: string; controlURL: string; tags: string[] }) =>
    request<TailscaleSnapshot>('/api/tailscale/login', json('POST', body)),
  tailscaleStart: () => request<TailscaleSnapshot>('/api/tailscale/start', { method: 'POST' }),
  tailscaleStop: () => request<TailscaleSnapshot>('/api/tailscale/stop', { method: 'POST' }),
  tailscaleLogout: () => request<TailscaleSnapshot>('/api/tailscale/logout', { method: 'POST' }),
  tailscalePeers: () => request<{ peers: Peer[] }>('/api/tailscale/peers'),
  ping: (ip: string) => request<{ results: PingResult[] }>('/api/tailscale/ping', json('POST', { ip })),
  netcheck: () => request<NetcheckReport>('/api/tailscale/netcheck'),
  captureStart: (id: string) => request<{ base: string; capture: CaptureStatus }>(`/api/bridges/${encodeURIComponent(id)}/capture/start`, { method: 'POST' }),
  captureStop: (id: string) => request<{ capture: CaptureStatus }>(`/api/bridges/${encodeURIComponent(id)}/capture/stop`, { method: 'POST' }),
  captures: () => request<{ files: CaptureFile[] }>('/api/captures'),
  deleteCapture: (name: string) => request<unknown>(`/api/captures/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  updateStatus: () => request<UpdateStatus>('/api/update/status'),
  updateCheck: () => request<UpdateStatus>('/api/update/check', { method: 'POST' }),
  updateDownload: () => request<UpdateStatus>('/api/update/download', { method: 'POST' }),
  updateCancel: () => request<UpdateStatus>('/api/update/cancel', { method: 'POST' }),
  updateClear: () => request<UpdateStatus>('/api/update/downloaded', { method: 'DELETE' }),
  updateApply: () => request<{ result: DeployResult }>('/api/update/apply', { method: 'POST' }),
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
