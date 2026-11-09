<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { NAlert, NButton, NButtonGroup, NCheckbox, NInput, NInputGroup, NSelect, NSwitch, NTag, NText, NTooltip, useMessage } from 'naive-ui'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useRoute } from 'vue-router'
import { useStatus } from '../status'
import { formatBytes } from '../api'
import { t } from '../i18n'
import { DIR_RX, isTx, MonitorRenderer, parseFrame, parseHex, type TrafficRecord } from '../monitor'

const { status } = useStatus()
const message = useMessage()
const route = useRoute()

// ---- 偏好（每个浏览器各自记住） ----
const PREF_KEY = 'rsagent.term'
interface Prefs {
  view: 'monitor' | 'terminal'
  showRx: boolean
  showTx: boolean
  timestamp: boolean
  hex: boolean
  gapMs: number
  lineEnding: '' | '\r' | '\n' | '\r\n'
  sendHex: boolean
  sendTo: 'serial' | 'client' // 送出列发往串口，或发给当前连接的 TCP 客户端（网络端）
}
const prefs = reactive<Prefs>({ view: 'monitor', showRx: true, showTx: true, timestamp: true, hex: false, gapMs: 100, lineEnding: '\r\n', sendHex: false, sendTo: 'serial' })
try {
  Object.assign(prefs, JSON.parse(localStorage.getItem(PREF_KEY) || '{}'))
} catch {
  // 忽略
}
watch(prefs, () => {
  try {
    localStorage.setItem(PREF_KEY, JSON.stringify(prefs))
  } catch {
    // 忽略
  }
})

// 从桥接页“终端”按钮跳转时带 ?id=，优先选中它
const bridgeId = ref<string>(typeof route.query.id === 'string' ? route.query.id : '')
const readWrite = ref(false)
const connected = ref(false)
const termStatus = ref<{ state: string; deviceOpen: boolean; active: string; dtr: boolean; rts: boolean; client?: string; viewers: number } | null>(null)
const paused = ref(false)
const rxBytes = ref(0)
const txBytes = ref(0)

const termEl = ref<HTMLDivElement | null>(null)
let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
let resizeObs: ResizeObserver | null = null

// ---- 流量记录：切换显示选项时据此重绘 ----
const RECORD_LIMIT = 4 << 20
let records: TrafficRecord[] = []
let recordBytes = 0
let renderer = new MonitorRenderer({ ...prefs })

const bridgeOptions = computed(() => (status.value?.bridges ?? []).map((b) => ({ label: `${b.name || b.id}  (${b.resolvedPort || b.port}, ${b.active})`, value: b.id })))
const eolOptions = computed(() => [
  { label: t('term.eolCRLF'), value: '\r\n' },
  { label: t('term.eolLF'), value: '\n' },
  { label: t('term.eolCR'), value: '\r' },
  { label: t('term.eolNone'), value: '' },
])
const sendToOptions = computed(() => [
  { label: t('term.toSerial'), value: 'serial' },
  { label: t('term.toClient'), value: 'client' },
])
const formatOptions = computed(() => [
  { label: t('term.text'), value: 'text' },
  { label: 'HEX', value: 'hex' },
])
const sendFormat = computed({
  get: () => (prefs.sendHex ? 'hex' : 'text'),
  set: (v: string) => {
    prefs.sendHex = v === 'hex'
  },
})
const gapOptions = computed(() => [
  { label: t('term.gapOff'), value: 0 },
  ...[10, 20, 50, 100, 200, 500, 1000].map((n) => ({ label: t('term.gap', { n }), value: n })),
])

function ensureTerm() {
  if (term || !termEl.value) return
  term = new Terminal({
    convertEol: false,
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Consolas, "Cascadia Mono", Menlo, monospace',
    scrollback: 20000,
    theme: { background: '#101418' },
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(termEl.value)
  fit.fit()
  // 只有“终端机”视图把按键直接送往装置；监看视图从下方输入框送出
  term.onData((d) => {
    if (prefs.view !== 'terminal' || !readWrite.value) return
    sendBytes(new TextEncoder().encode(d === '\r' ? prefs.lineEnding || '\r' : d))
  })
  resizeObs = new ResizeObserver(() => fit?.fit())
  resizeObs.observe(termEl.value)
}

function renderRecord(r: TrafficRecord): string | Uint8Array {
  if (prefs.view === 'terminal') return r.dir === DIR_RX ? r.data : ''
  return renderer.render(r)
}

function addRecords(list: TrafficRecord[]) {
  for (const r of list) {
    // 帧里的数据是整块缓冲区的视图，复制出来以便单独保留
    const rec = { ...r, data: r.data.slice() }
    records.push(rec)
    recordBytes += rec.data.length
    if (rec.dir === DIR_RX) rxBytes.value += rec.data.length
    else if (isTx(rec.dir)) txBytes.value += rec.data.length
    if (!paused.value && term) {
      const out = renderRecord(rec)
      if (out.length) term.write(out)
    }
  }
  if (recordBytes > RECORD_LIMIT) {
    let drop = 0
    while (recordBytes > RECORD_LIMIT * 0.75 && drop < records.length - 1) recordBytes -= records[drop++].data.length
    records = records.slice(drop)
  }
}

// rerender 按当前选项从记录重画整个画面。
function rerender() {
  if (!term) return
  renderer = new MonitorRenderer({ ...prefs })
  term.reset()
  if (paused.value) return
  // 拼成大块写入，比逐条写快得多
  let text = ''
  for (const r of records) {
    const out = renderRecord(r)
    if (typeof out === 'string') text += out
    else {
      if (text) term.write(text)
      text = ''
      term.write(out)
    }
    if (text.length > 256 << 10) {
      term.write(text)
      text = ''
    }
  }
  if (text) term.write(text)
}

watch(() => [prefs.view, prefs.showRx, prefs.showTx, prefs.timestamp, prefs.hex, prefs.gapMs], rerender)
watch(paused, (p) => {
  if (!p) rerender()
})

function connect() {
  disconnect()
  if (!bridgeId.value) {
    message.warning(t('term.selectBridge'))
    return
  }
  ensureTerm()
  clearScreen()
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const sock = new WebSocket(`${proto}://${location.host}/ws/term/${encodeURIComponent(bridgeId.value)}?mode=${readWrite.value ? 'rw' : 'ro'}`)
  sock.binaryType = 'arraybuffer'
  sock.onopen = () => {
    connected.value = true
    if (prefs.view === 'terminal') term?.focus()
  }
  sock.onmessage = (ev) => {
    if (ws !== sock) return
    if (ev.data instanceof ArrayBuffer) addRecords(parseFrame(ev.data))
    else {
      try {
        const m = JSON.parse(ev.data)
        if (m.type === 'status') termStatus.value = m
      } catch {
        // 忽略
      }
    }
  }
  sock.onclose = () => {
    if (ws === sock) {
      ws = null
      connected.value = false
    }
  }
  sock.onerror = () => sock.close()
  ws = sock
}

function disconnect() {
  if (ws) {
    ws.close()
    ws = null
  }
  connected.value = false
}

function canSend(): boolean {
  if (!ws || ws.readyState !== WebSocket.OPEN) return false
  if (!readWrite.value) {
    message.warning(t('term.roSend'))
    return false
  }
  return true
}

function sendControl(cmd: Record<string, unknown>) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return
  if (!readWrite.value) {
    message.warning(t('term.roNoLines'))
    return
  }
  ws.send(JSON.stringify(cmd))
}

function sendBytes(b: Uint8Array<ArrayBuffer>) {
  if (ws && ws.readyState === WebSocket.OPEN && readWrite.value && b.length) ws.send(b)
}

function toBase64(b: Uint8Array): string {
  let s = ''
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000))
  return btoa(s)
}

// 发给当前连接的 TCP 客户端，像是设备输出的一样
function sendToClient(b: Uint8Array): boolean {
  if (!termStatus.value?.client) {
    message.warning(t('term.noClient'))
    return false
  }
  if (ws && ws.readyState === WebSocket.OPEN && readWrite.value && b.length) ws.send(JSON.stringify({ type: 'client', data: toBase64(b) }))
  return true
}

// ---- 下方送出列 ----
const HISTORY_KEY = 'rsagent.term.history'
const sendInput = ref('')
let history: string[] = []
try {
  history = JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]')
} catch {
  history = []
}
let historyPos = -1

function send() {
  const v = sendInput.value
  if (!canSend()) return
  let bytes: Uint8Array<ArrayBuffer>
  if (prefs.sendHex) {
    const b = parseHex(v)
    if (!b) {
      message.error(t('term.badHex'))
      return
    }
    bytes = b
  } else {
    bytes = new TextEncoder().encode(v + prefs.lineEnding)
    if (!bytes.length) return
  }
  if (prefs.sendTo === 'client') {
    if (!sendToClient(bytes)) return
  } else {
    sendBytes(bytes)
  }
  if (v) {
    history = [v, ...history.filter((h) => h !== v)].slice(0, 50)
    try {
      localStorage.setItem(HISTORY_KEY, JSON.stringify(history))
    } catch {
      // 忽略
    }
  }
  historyPos = -1
  sendInput.value = ''
}

function onSendKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.isComposing) {
    e.preventDefault()
    send()
  } else if (e.key === 'ArrowUp' && history.length) {
    e.preventDefault()
    historyPos = Math.min(historyPos + 1, history.length - 1)
    sendInput.value = history[historyPos]
  } else if (e.key === 'ArrowDown' && historyPos >= 0) {
    e.preventDefault()
    historyPos--
    sendInput.value = historyPos >= 0 ? history[historyPos] : ''
  }
}

function clearScreen() {
  records = []
  recordBytes = 0
  rxBytes.value = 0
  txBytes.value = 0
  renderer = new MonitorRenderer({ ...prefs })
  term?.reset()
}

function downloadBuffer() {
  if (!term) return
  // 自动折行的行接回上一行，保留原本的一行一条
  const lines: string[] = []
  const buf = term.buffer.active
  for (let i = 0; i < buf.length; i++) {
    const line = buf.getLine(i)
    if (!line) continue
    const s = line.translateToString(true)
    if (line.isWrapped && lines.length) lines[lines.length - 1] += s
    else lines.push(s)
  }
  while (lines.length && !lines[lines.length - 1]) lines.pop()
  const blob = new Blob([lines.join('\n') + '\n'], { type: 'text/plain;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `${bridgeId.value}-${new Date().toISOString().replace(/[:.]/g, '-')}.txt`
  a.click()
  URL.revokeObjectURL(a.href)
}

watch(readWrite, () => {
  if (connected.value) connect()
})
watch(
  () => prefs.view,
  (v) => {
    if (v === 'terminal') term?.focus()
  },
)

onMounted(async () => {
  await nextTick()
  ensureTerm()
  if (!bridgeId.value && status.value?.bridges.length) bridgeId.value = status.value.bridges[0].id
})
watch(
  () => status.value?.bridges.length,
  () => {
    if (!bridgeId.value && status.value?.bridges.length) bridgeId.value = status.value.bridges[0].id
  },
)
onBeforeUnmount(() => {
  disconnect()
  resizeObs?.disconnect()
  term?.dispose()
  term = null
})
</script>

<template>
  <div class="page">
    <div class="bar">
      <NSelect v-model:value="bridgeId" :options="bridgeOptions" style="width: 300px" :placeholder="t('term.selectBridge')" :disabled="connected" />
      <NButton v-if="!connected" type="primary" @click="connect">{{ t('term.connect') }}</NButton>
      <NButton v-else @click="disconnect">{{ t('term.disconnect') }}</NButton>
      <NSwitch v-model:value="readWrite"><template #checked>{{ t('term.rw') }}</template><template #unchecked>{{ t('term.ro') }}</template></NSwitch>
      <NTag size="small" :type="connected ? 'success' : 'default'">{{ connected ? t('term.connected') : t('term.disconnected') }}</NTag>
      <template v-if="termStatus">
        <NTag size="small" :type="termStatus.deviceOpen ? 'success' : 'warning'">{{ termStatus.deviceOpen ? t('term.deviceOnline') : t('term.deviceOffline') }}</NTag>
        <NText depth="3" class="mono">{{ termStatus.active }}</NText>
        <NText v-if="termStatus.client" depth="3">{{ t('term.tcpClient', { c: termStatus.client }) }}</NText>
        <NText depth="3">{{ t('term.viewers', { n: termStatus.viewers }) }}</NText>
      </template>
      <span class="grow" />
      <NText depth="3" class="mono">{{ t('term.counters', { rx: formatBytes(rxBytes), tx: formatBytes(txBytes) }) }}</NText>
    </div>

    <div class="bar">
      <NButtonGroup size="small">
        <NTooltip>
          <template #trigger>
            <NButton :type="prefs.view === 'monitor' ? 'primary' : 'default'" @click="prefs.view = 'monitor'">{{ t('term.modeMonitor') }}</NButton>
          </template>
          {{ t('term.modeMonitorTip') }}
        </NTooltip>
        <NTooltip>
          <template #trigger>
            <NButton :type="prefs.view === 'terminal' ? 'primary' : 'default'" @click="prefs.view = 'terminal'">{{ t('term.modeTerminal') }}</NButton>
          </template>
          {{ t('term.modeTerminalTip') }}
        </NTooltip>
      </NButtonGroup>
      <template v-if="prefs.view === 'monitor'">
        <NTooltip>
          <template #trigger>
            <NCheckbox v-model:checked="prefs.showRx"><span class="rx">RX</span></NCheckbox>
          </template>
          <div><span class="rx">■</span> {{ t('term.rxTip') }}</div>
          <div><span class="rxweb">■</span> {{ t('term.rxWebTip') }}</div>
        </NTooltip>
        <NTooltip>
          <template #trigger>
            <NCheckbox v-model:checked="prefs.showTx"><span class="tx">TX</span></NCheckbox>
          </template>
          <div><span class="tx">■</span> {{ t('term.txTip') }}</div>
          <div><span class="txweb">■</span> {{ t('term.txWebTip') }}</div>
        </NTooltip>
        <NCheckbox v-model:checked="prefs.timestamp">{{ t('term.timestamp') }}</NCheckbox>
        <NSwitch v-model:value="prefs.hex" size="small"><template #checked>HEX</template><template #unchecked>{{ t('term.text') }}</template></NSwitch>
        <NSelect v-model:value="prefs.gapMs" :options="gapOptions" size="small" style="width: 210px" />
        <NButton size="small" :type="paused ? 'warning' : 'default'" @click="paused = !paused">{{ paused ? t('term.resume') : t('term.pause') }}</NButton>
      </template>
      <span class="grow" />
      <NButton size="small" :disabled="!connected" @click="sendControl({ type: 'break' })">{{ t('term.break') }}</NButton>
      <NButton size="small" :disabled="!connected" :type="termStatus?.dtr ? 'success' : 'default'" @click="sendControl({ type: 'dtr', on: !termStatus?.dtr })">DTR</NButton>
      <NButton size="small" :disabled="!connected" :type="termStatus?.rts ? 'success' : 'default'" @click="sendControl({ type: 'rts', on: !termStatus?.rts })">RTS</NButton>
      <NButton size="small" @click="clearScreen">{{ t('term.clear') }}</NButton>
      <NButton size="small" @click="downloadBuffer">{{ t('term.download') }}</NButton>
    </div>

    <NAlert v-if="!status?.bridges.length" type="info">{{ t('term.noBridges') }}</NAlert>
    <NAlert v-if="paused && prefs.view === 'monitor'" type="warning" :show-icon="false" class="slim">{{ t('term.paused') }}</NAlert>
    <div ref="termEl" class="term" />

    <div class="bar">
      <NInputGroup>
        <NSelect v-model:value="prefs.sendTo" :options="sendToOptions" style="width: 150px" :aria-label="t('term.sendTo')" />
        <NSelect v-model:value="sendFormat" :options="formatOptions" style="width: 90px" />
        <NInput
          v-model:value="sendInput"
          class="mono"
          :placeholder="readWrite ? (prefs.sendHex ? t('term.sendPhHex') : t('term.sendPh')) : t('term.roSend')"
          :disabled="!connected || !readWrite"
          @keydown="onSendKey"
        />
        <NSelect v-model:value="prefs.lineEnding" :options="eolOptions" :disabled="prefs.sendHex" style="width: 130px" />
        <NButton type="primary" :disabled="!connected || !readWrite" @click="send">{{ t('term.send') }}</NButton>
      </NInputGroup>
    </div>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 10px;
  height: calc(100vh - 120px);
  min-height: 480px;
}
.bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}
.grow {
  flex: 1;
}
.term {
  flex: 1;
  min-height: 240px;
  background: #101418;
  border-radius: 4px;
  padding: 6px;
  overflow: hidden;
}
.slim {
  padding: 2px 10px;
}
.mono {
  font-family: Consolas, 'Cascadia Mono', Menlo, monospace;
}
.rx {
  color: #18a058;
  font-weight: 600;
}
.tx {
  color: #d4a017;
  font-weight: 600;
}
.txweb {
  color: #2bb3c0;
  font-weight: 600;
}
.rxweb {
  color: #d670d6;
  font-weight: 600;
}
</style>
