// 全局状态：通过 /ws/events 接收推送，断开后退回轮询。
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from './api'
import type { Status } from './api'

const status = ref<Status | null>(null)
const connected = ref(false)
const error = ref('')

let ws: WebSocket | null = null
let refCount = 0
let pollTimer: number | undefined
let reconnectTimer: number | undefined

function connect() {
  if (ws) return
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const sock = new WebSocket(`${proto}://${location.host}/ws/events`)
  ws = sock
  sock.onopen = () => {
    connected.value = true
    error.value = ''
  }
  sock.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.type === 'status') status.value = msg.status
    } catch {
      // 忽略坏消息
    }
  }
  sock.onclose = () => {
    connected.value = false
    ws = null
    if (refCount > 0) reconnectTimer = window.setTimeout(connect, 2000)
  }
  sock.onerror = () => {
    sock.close()
  }
}

async function poll() {
  if (connected.value) return
  try {
    status.value = await api.status()
    error.value = ''
  } catch (e) {
    error.value = String(e)
  }
}

function acquire() {
  refCount++
  if (refCount === 1) {
    poll()
    connect()
    pollTimer = window.setInterval(poll, 5000)
  }
}

function release() {
  refCount--
  if (refCount <= 0) {
    refCount = 0
    if (pollTimer) window.clearInterval(pollTimer)
    if (reconnectTimer) window.clearTimeout(reconnectTimer)
    ws?.close()
    ws = null
  }
}

/** 在组件中使用：挂载时订阅，卸载时释放。 */
export function useStatus() {
  onMounted(acquire)
  onUnmounted(release)
  return { status, connected, error, refresh: poll }
}
