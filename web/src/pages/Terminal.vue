<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NCard, NSelect, NSpace, NSwitch, NTag, NText, useMessage } from 'naive-ui'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useStatus } from '../status'

const { status } = useStatus()
const message = useMessage()

const bridgeId = ref<string>('')
const readWrite = ref(false)
const lineEnding = ref<'\r' | '\n' | '\r\n'>('\r\n')
const connected = ref(false)
const termStatus = ref<{ state: string; deviceOpen: boolean; active: string; dtr: boolean; rts: boolean; client?: string; viewers: number } | null>(null)
const hexView = ref(false)

const termEl = ref<HTMLDivElement | null>(null)
let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
let resizeObs: ResizeObserver | null = null

const bridgeOptions = computed(() => (status.value?.bridges ?? []).map((b) => ({ label: `${b.name || b.id}  (${b.resolvedPort || b.port}, ${b.active})`, value: b.id })))
const lineOptions = [
  { label: '回车发送 CRLF', value: '\r\n' },
  { label: '回车发送 LF', value: '\n' },
  { label: '回车发送 CR', value: '\r' },
]

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
  term.onData((d) => {
    if (!ws || ws.readyState !== WebSocket.OPEN || !readWrite.value) return
    const data = d === '\r' ? lineEnding.value : d
    ws.send(new TextEncoder().encode(data))
  })
  resizeObs = new ResizeObserver(() => fit?.fit())
  resizeObs.observe(termEl.value)
}

function writeBytes(buf: ArrayBuffer) {
  if (!term) return
  const bytes = new Uint8Array(buf)
  if (hexView.value) {
    let line = ''
    for (let i = 0; i < bytes.length; i++) {
      line += bytes[i].toString(16).padStart(2, '0') + ' '
      if ((i + 1) % 16 === 0) line += '\r\n'
    }
    term.write(line)
  } else {
    term.write(bytes)
  }
}

function connect() {
  disconnect()
  if (!bridgeId.value) {
    message.warning('请选择桥接')
    return
  }
  ensureTerm()
  term?.clear()
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const sock = new WebSocket(`${proto}://${location.host}/ws/term/${encodeURIComponent(bridgeId.value)}?mode=${readWrite.value ? 'rw' : 'ro'}`)
  sock.binaryType = 'arraybuffer'
  sock.onopen = () => {
    connected.value = true
    term?.focus()
  }
  sock.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) writeBytes(ev.data)
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
    connected.value = false
    if (ws === sock) ws = null
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

function sendControl(cmd: Record<string, unknown>) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return
  if (!readWrite.value) {
    message.warning('只读模式下不能操作控制线')
    return
  }
  ws.send(JSON.stringify(cmd))
}

function sendText(text: string) {
  if (!ws || ws.readyState !== WebSocket.OPEN || !readWrite.value) return
  ws.send(new TextEncoder().encode(text))
}

const quickInput = ref('')
function sendQuick() {
  if (!quickInput.value) return
  sendText(quickInput.value + lineEnding.value)
  quickInput.value = ''
}

function clearScreen() {
  term?.clear()
}

function downloadBuffer() {
  if (!term) return
  const lines: string[] = []
  const buf = term.buffer.active
  for (let i = 0; i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? '')
  const blob = new Blob([lines.join('\n')], { type: 'text/plain;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `${bridgeId.value}-${new Date().toISOString().replace(/[:.]/g, '-')}.txt`
  a.click()
  URL.revokeObjectURL(a.href)
}

watch(readWrite, () => {
  if (connected.value) connect()
})

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
  <NSpace vertical :size="12" style="height: calc(100vh - 120px)">
    <NCard size="small">
      <NSpace align="center">
        <NSelect v-model:value="bridgeId" :options="bridgeOptions" style="width: 320px" placeholder="选择桥接" :disabled="connected" />
        <NButton v-if="!connected" type="primary" @click="connect">连接</NButton>
        <NButton v-else @click="disconnect">断开</NButton>
        <NSwitch v-model:value="readWrite"><template #checked>读写</template><template #unchecked>只读</template></NSwitch>
        <NSelect v-model:value="lineEnding" :options="lineOptions" style="width: 160px" size="small" />
        <NSwitch v-model:value="hexView" size="small"><template #checked>HEX</template><template #unchecked>文本</template></NSwitch>
        <NButton size="small" :disabled="!connected" @click="sendControl({ type: 'break' })">Break</NButton>
        <NButton size="small" :disabled="!connected" :type="termStatus?.dtr ? 'success' : 'default'" @click="sendControl({ type: 'dtr', on: !termStatus?.dtr })">DTR</NButton>
        <NButton size="small" :disabled="!connected" :type="termStatus?.rts ? 'success' : 'default'" @click="sendControl({ type: 'rts', on: !termStatus?.rts })">RTS</NButton>
        <NButton size="small" @click="clearScreen">清屏</NButton>
        <NButton size="small" @click="downloadBuffer">下载</NButton>
      </NSpace>
      <NSpace align="center" style="margin-top: 8px">
        <NTag size="small" :type="connected ? 'success' : 'default'">{{ connected ? '已连接' : '未连接' }}</NTag>
        <template v-if="termStatus">
          <NTag size="small" :type="termStatus.deviceOpen ? 'success' : 'warning'">{{ termStatus.deviceOpen ? '设备在线' : '设备离线' }}</NTag>
          <NText depth="3">{{ termStatus.active }}</NText>
          <NText v-if="termStatus.client" depth="3">TCP 客户端：{{ termStatus.client }}</NText>
          <NText depth="3">旁观 {{ termStatus.viewers }}</NText>
        </template>
        <input v-model="quickInput" class="quick" placeholder="输入一行后回车发送（读写模式）" :disabled="!connected || !readWrite" @keydown.enter="sendQuick" />
      </NSpace>
    </NCard>
    <NAlert v-if="!status?.bridges.length" type="info">还没有桥接，请先在"串口桥接"页新增。</NAlert>
    <div ref="termEl" class="term" />
  </NSpace>
</template>

<style scoped>
.term {
  flex: 1;
  min-height: 360px;
  height: 100%;
  background: #101418;
  border-radius: 4px;
  padding: 6px;
}
.quick {
  width: 360px;
  padding: 4px 8px;
  border: 1px solid rgba(128, 128, 128, 0.4);
  border-radius: 3px;
  background: transparent;
  color: inherit;
  font-family: monospace;
}
</style>
