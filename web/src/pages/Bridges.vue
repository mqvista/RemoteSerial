<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NDataTable,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NPopconfirm,
  NSelect,
  NSpace,
  NTag,
  NText,
  useMessage,
} from 'naive-ui'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import { api, BAUD_RATES, formatBytes } from '../api'
import type { BridgeConfig, BridgeStatus, CaptureFile, SerialPort } from '../api'
import { useStatus } from '../status'

const { status } = useStatus()
const message = useMessage()
const router = useRouter()

const configs = ref<BridgeConfig[]>([])
const ports = ref<SerialPort[]>([])
const captures = ref<CaptureFile[]>([])

async function loadCaptures() {
  try {
    captures.value = (await api.captures()).files
  } catch {
    // 忽略
  }
}

async function toggleCapture(id: string) {
  const s = statusById.value[id]
  busy.value = true
  try {
    if (s?.capture?.active) {
      await api.captureStop(id)
      message.success('已停止录制')
    } else {
      const r = await api.captureStart(id)
      message.success(`开始录制：${r.base}`)
    }
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
    loadCaptures()
  }
}

async function deleteCapture(name: string) {
  try {
    await api.deleteCapture(name)
    message.success('已删除')
  } catch (e) {
    message.error(String(e))
  } finally {
    loadCaptures()
  }
}

const captureColumns: DataTableColumns<CaptureFile> = [
  { title: '文件', key: 'name', minWidth: 220, ellipsis: { tooltip: true } },
  { title: '大小', key: 'size', width: 100, render: (f) => formatBytes(f.size) },
  { title: '时间', key: 'modTime', width: 170, render: (f) => new Date(f.modTime).toLocaleString('zh-CN', { hour12: false }) },
  {
    title: '操作',
    key: 'actions',
    width: 130,
    render: (f) =>
      h(NSpace, { size: 4, wrap: false }, {
        default: () => [
          h('a', { href: `/api/captures/${encodeURIComponent(f.name)}`, download: f.name, style: 'font-size:12px' }, '下载'),
          h(NPopconfirm, { onPositiveClick: () => deleteCapture(f.name) }, { trigger: () => h(NButton, { size: 'tiny', type: 'error', ghost: true }, { default: () => '删除' }), default: () => '删除此文件？' }),
        ],
      }),
  },
]

const portColumns: DataTableColumns<SerialPort> = [
  { title: '串口', key: 'name', width: 110 },
  { title: '类型', key: 'isUSB', width: 70, render: (p) => (p.isUSB ? 'USB' : '板载') },
  { title: '芯片', key: 'chip', minWidth: 160, ellipsis: { tooltip: true }, render: (p) => p.chip || '-' },
  { title: 'VID:PID', key: 'vidpid', width: 100, render: (p) => (p.vid ? `${p.vid}:${p.pid}` : '-') },
  { title: '序列号', key: 'serialNumber', minWidth: 120, ellipsis: { tooltip: true }, render: (p) => p.serialNumber || '-' },
  { title: '占用', key: 'usedBy', width: 100, ellipsis: { tooltip: true }, render: (p) => p.usedBy || '-' },
]

const busy = ref(false)
const showEditor = ref(false)
const editing = ref<BridgeConfig | null>(null)
const editingIsNew = ref(false)
const matchByUSB = ref(false)

const nodeRunning = computed(() => status.value?.tailscale.state === 'Running')
const statusById = computed(() => {
  const m: Record<string, BridgeStatus> = {}
  for (const b of status.value?.bridges ?? []) m[b.id] = b
  return m
})

async function load() {
  try {
    configs.value = (await api.bridges()).bridges.map((b) => b.config)
  } catch (e) {
    message.error(String(e))
  }
}

async function loadPorts() {
  try {
    ports.value = (await api.serialPorts()).ports
  } catch (e) {
    message.error(String(e))
  }
}

const portOptions = computed<SelectOption[]>(() =>
  ports.value.map((p) => {
    const extra = [p.chip, p.vid && p.pid ? `${p.vid}:${p.pid}` : '', p.serialNumber ? `SN ${p.serialNumber}` : '', p.usedBy ? `已被 ${p.usedBy} 占用` : '']
      .filter(Boolean)
      .join(' · ')
    return { label: extra ? `${p.name}  (${extra})` : p.name, value: p.name }
  }),
)

const baudOptions = BAUD_RATES.map((b) => ({ label: String(b), value: b }))
const parityOptions = [
  { label: '无 (N)', value: 'N' },
  { label: '偶 (E)', value: 'E' },
  { label: '奇 (O)', value: 'O' },
  { label: 'Mark (M)', value: 'M' },
  { label: 'Space (S)', value: 'S' },
]
const stopOptions = [
  { label: '1', value: 1 },
  { label: '1.5', value: 1.5 },
  { label: '2', value: 2 },
]
const modeOptions = [
  { label: '裸 TCP：任何 TCP 客户端可连，参数固定', value: 'raw' },
  { label: 'RFC2217：客户端可改波特率、拉 DTR/RTS', value: 'rfc2217' },
]
const policyOptions = [
  { label: '新连接替换旧连接', value: 'replace' },
  { label: '拒绝新连接', value: 'reject' },
]

type TagType = 'default' | 'info' | 'success' | 'warning' | 'error'
const stateMap: Record<string, [string, TagType]> = {
  Disabled: ['已停用', 'default'],
  WaitingNetwork: ['等待节点上线', 'warning'],
  WaitingDevice: ['等待设备', 'warning'],
  Listening: ['监听中', 'info'],
  Connected: ['已连接', 'success'],
  Error: ['错误', 'error'],
}
function stateInfo(s?: BridgeStatus): { label: string; type: TagType } {
  if (!s) return { label: '未知', type: 'default' }
  const [label, type] = stateMap[s.state] ?? [s.state, 'default']
  return { label, type }
}

function paramsText(c: BridgeConfig): string {
  const s = statusById.value[c.id]
  if (s && s.paramsBy === 'client' && s.active) return s.active
  return `${c.baud} ${c.dataBits}${c.parity}${c.stopBits}`
}

function clientSince(s?: BridgeStatus): string {
  if (!s?.clientSince) return ''
  const sec = Math.max(0, Math.floor((Date.now() - new Date(s.clientSince).getTime()) / 1000))
  if (sec < 60) return `${sec} 秒`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟`
  return `${Math.floor(sec / 3600)} 小时 ${Math.floor((sec % 3600) / 60)} 分`
}

function nextTCPPort(): number {
  const used = new Set(configs.value.map((c) => c.tcpPort))
  let p = 2217
  while (used.has(p)) p++
  return p
}

function nextID(): string {
  const used = new Set(configs.value.map((c) => c.id))
  let i = 1
  while (used.has(`b${i}`)) i++
  return `b${i}`
}

function openNew() {
  editing.value = {
    id: nextID(),
    name: '',
    port: ports.value[0]?.name ?? '',
    match: null,
    baud: 115200,
    dataBits: 8,
    parity: 'N',
    stopBits: 1,
    mode: 'rfc2217',
    tcpPort: nextTCPPort(),
    localPort: 0,
    clientPolicy: 'replace',
    idleTimeoutSec: 0,
    enabled: true,
  }
  matchByUSB.value = false
  editingIsNew.value = true
  showEditor.value = true
  loadPorts()
}

function openEdit(c: BridgeConfig) {
  editing.value = JSON.parse(JSON.stringify(c))
  matchByUSB.value = !!c.match
  editingIsNew.value = false
  showEditor.value = true
  loadPorts()
}

function onMatchToggle(v: boolean) {
  matchByUSB.value = v
  if (!editing.value) return
  if (v) {
    const p = ports.value.find((x) => x.name === editing.value!.port)
    editing.value.match = { vid: p?.vid ?? '', pid: p?.pid ?? '', serial: p?.serialNumber ?? '' }
  } else {
    editing.value.match = null
  }
}

async function saveAll(list: BridgeConfig[], okMsg: string) {
  busy.value = true
  try {
    configs.value = (await api.saveBridges(list)).bridges.map((b) => b.config)
    message.success(okMsg)
    return true
  } catch (e) {
    message.error(String(e))
    return false
  } finally {
    busy.value = false
  }
}

async function submitEditor() {
  if (!editing.value) return
  const e = editing.value
  if (!e.name.trim()) e.name = e.port || e.id
  if (!e.port && !e.match) {
    message.warning('请选择串口')
    return
  }
  const list = configs.value.filter((c) => c.id !== e.id)
  list.push(e)
  list.sort((a, b) => a.tcpPort - b.tcpPort)
  if (await saveAll(list, editingIsNew.value ? '已添加' : '已保存')) showEditor.value = false
}

async function remove(id: string) {
  await saveAll(
    configs.value.filter((c) => c.id !== id),
    '已删除',
  )
}

async function action(id: string, act: 'start' | 'stop' | 'kick') {
  busy.value = true
  try {
    const r = await api.bridgeAction(id, act)
    configs.value = r.bridges.map((b) => b.config)
    if (r.warning) message.warning(r.warning)
    else message.success({ start: '已启动', stop: '已停止', kick: '已断开客户端' }[act])
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
  }
}

function openTerminal(id: string) {
  router.push({ name: 'terminal', query: { id } })
}

onMounted(() => {
  load()
  loadPorts()
  loadCaptures()
})
</script>

<template>
  <NSpace vertical :size="16">
    <NAlert v-if="!nodeRunning" type="warning">Tailscale 节点未上线，桥接会在节点上线后自动开始监听。</NAlert>
    <NCard title="串口桥接">
      <template #header-extra>
        <NSpace :size="8">
          <NButton size="small" @click="loadPorts">刷新串口</NButton>
          <NButton size="small" type="primary" @click="openNew">新增桥接</NButton>
        </NSpace>
      </template>

      <NEmpty v-if="!configs.length" description="还没有桥接，点右上角“新增桥接”" />
      <div v-else class="bridge-list">
        <div v-for="c in configs" :key="c.id" class="bridge-item" :class="{ disabled: !c.enabled }">
          <div class="bridge-head">
            <div class="bridge-title">
              <span class="bridge-name" :title="c.name || c.id">{{ c.name || c.id }}</span>
              <NTag size="small" :type="stateInfo(statusById[c.id]).type" :bordered="false">{{ stateInfo(statusById[c.id]).label }}</NTag>
              <NTag size="small" :bordered="false">{{ c.mode === 'rfc2217' ? 'RFC2217' : '裸 TCP' }}</NTag>
              <NTag v-if="statusById[c.id]?.capture?.active" size="small" type="warning" :bordered="false">录制中</NTag>
            </div>
            <div class="bridge-actions">
              <NButton v-if="c.enabled" size="tiny" @click="action(c.id, 'stop')">停止</NButton>
              <NButton v-else size="tiny" type="primary" @click="action(c.id, 'start')">启动</NButton>
              <NButton size="tiny" :disabled="!c.enabled" @click="openTerminal(c.id)">终端</NButton>
              <NButton size="tiny" :disabled="!statusById[c.id]?.client" @click="action(c.id, 'kick')">踢掉</NButton>
              <NButton size="tiny" :type="statusById[c.id]?.capture?.active ? 'warning' : 'default'" :disabled="!c.enabled" @click="toggleCapture(c.id)">
                {{ statusById[c.id]?.capture?.active ? '停止录制' : '录制' }}
              </NButton>
              <NButton size="tiny" @click="openEdit(c)">编辑</NButton>
              <NPopconfirm @positive-click="remove(c.id)">
                <template #trigger>
                  <NButton size="tiny" type="error" ghost>删除</NButton>
                </template>
                删除此桥接？
              </NPopconfirm>
            </div>
          </div>

          <div class="bridge-meta">
            <div class="meta">
              <span class="k">串口</span>
              <span class="v">
                {{ statusById[c.id]?.resolvedPort || c.port || '(按 USB 匹配)' }}
                <NText v-if="c.match" depth="3" class="sub">USB 匹配</NText>
              </span>
            </div>
            <div class="meta">
              <span class="k">参数</span>
              <span class="v">
                {{ paramsText(c) }}
                <NTag v-if="statusById[c.id]?.paramsBy === 'client'" size="tiny" type="info" :bordered="false">客户端设置</NTag>
              </span>
            </div>
            <div class="meta">
              <span class="k">端口</span>
              <span class="v">
                {{ c.tcpPort }}
                <NText v-if="c.localPort" depth="3" class="sub">本机 {{ c.localPort }}</NText>
              </span>
            </div>
            <div class="meta">
              <span class="k">客户端</span>
              <span class="v">
                <template v-if="statusById[c.id]?.client">
                  {{ statusById[c.id].client }}
                  <NText depth="3" class="sub">{{ clientSince(statusById[c.id]) }}</NText>
                </template>
                <NText v-else depth="3">无</NText>
                <NText v-if="statusById[c.id]?.viewers" depth="3" class="sub">终端 {{ statusById[c.id].viewers }} 人</NText>
              </span>
            </div>
            <div class="meta">
              <span class="k">流量</span>
              <span class="v" title="设备→TCP / TCP→设备">
                <template v-if="statusById[c.id]">↓ {{ formatBytes(statusById[c.id].fromDevice) }} ↑ {{ formatBytes(statusById[c.id].toDevice) }}</template>
                <template v-else>-</template>
              </span>
            </div>
            <div v-if="statusById[c.id]?.deviceOpen" class="meta">
              <span class="k">控制线</span>
              <span class="v lines">
                <NTag size="tiny" :type="statusById[c.id].dtr ? 'success' : 'default'" :bordered="false">DTR</NTag>
                <NTag size="tiny" :type="statusById[c.id].rts ? 'success' : 'default'" :bordered="false">RTS</NTag>
              </span>
            </div>
          </div>
          <NText v-if="statusById[c.id]?.lastError" type="error" class="bridge-err">{{ statusById[c.id].lastError }}</NText>
        </div>
      </div>

      <template v-if="configs.length">
        <NText depth="3" style="display: block; margin-top: 12px; font-size: 12px">
          本地连接示例：裸 TCP 用 <code>nc {{ status?.tailscale.ip || '100.x.y.z' }} {{ configs[0].tcpPort }}</code> 或串口工具的 TCP 客户端模式；RFC2217 用
          <code>rfc2217://{{ status?.tailscale.ip || '100.x.y.z' }}:{{ configs[0].tcpPort }}</code>（pyserial、esptool、PlatformIO），需要 COMx 的工具用 com0com + hub4com 映射。
        </NText>
      </template>
    </NCard>

    <NCard title="录制文件">
      <template #header-extra>
        <NButton size="small" @click="loadCaptures">刷新</NButton>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
        录制把设备输出写入 .rx.bin，写入设备的数据写入 .tx.bin，原始字节，可用 hex 工具打开。文件在数据目录 logs 下。
      </NText>
      <NDataTable :columns="captureColumns" :data="captures" size="small" :bordered="false" :scroll-x="620" />
    </NCard>

    <NCard title="本机串口">
      <NDataTable :columns="portColumns" :data="ports" size="small" :bordered="false" :scroll-x="660" />
    </NCard>

    <NModal v-model:show="showEditor" preset="card" :title="editingIsNew ? '新增桥接' : '编辑桥接'" class="editor-modal">
      <NForm v-if="editing" label-placement="left" label-width="110">
        <NFormItem label="名称">
          <NInput v-model:value="editing.name" placeholder="如：主控板调试口" />
        </NFormItem>
        <NFormItem label="串口">
          <NSelect v-model:value="editing.port" :options="portOptions" filterable tag placeholder="选择或输入串口名" />
        </NFormItem>
        <NFormItem label="USB 匹配">
          <NSpace vertical :size="4">
            <NCheckbox :checked="matchByUSB" @update:checked="onMatchToggle">按 VID/PID/序列号匹配，COM 号变化时仍能找到设备</NCheckbox>
            <NSpace v-if="matchByUSB && editing.match">
              <NInput v-model:value="editing.match.vid" placeholder="VID" style="width: 100px" />
              <NInput v-model:value="editing.match.pid" placeholder="PID" style="width: 100px" />
              <NInput v-model:value="editing.match.serial" placeholder="序列号（可空）" style="width: 220px" />
            </NSpace>
          </NSpace>
        </NFormItem>
        <NFormItem label="波特率">
          <NSelect v-model:value="editing.baud" :options="baudOptions" filterable tag />
        </NFormItem>
        <NFormItem label="数据位 / 校验 / 停止位">
          <NSpace>
            <NInputNumber v-model:value="editing.dataBits" :min="5" :max="8" style="width: 90px" />
            <NSelect v-model:value="editing.parity" :options="parityOptions" style="width: 130px" />
            <NSelect v-model:value="editing.stopBits" :options="stopOptions" style="width: 90px" />
          </NSpace>
        </NFormItem>
        <NFormItem label="模式">
          <NSelect v-model:value="editing.mode" :options="modeOptions" />
        </NFormItem>
        <NFormItem label="TCP 端口">
          <NInputNumber v-model:value="editing.tcpPort" :min="1" :max="65535" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">tailnet 内访问 节点IP:端口</NText>
        </NFormItem>
        <NFormItem label="本机端口">
          <NInputNumber v-model:value="editing.localPort" :min="0" :max="65535" placeholder="0 为不监听" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">可选，同时在 127.0.0.1 监听，供远端本机的串口工具使用</NText>
        </NFormItem>
        <NFormItem label="客户端策略">
          <NSelect v-model:value="editing.clientPolicy" :options="policyOptions" />
        </NFormItem>
        <NFormItem label="空闲超时(秒)">
          <NInputNumber v-model:value="editing.idleTimeoutSec" :min="0" placeholder="0 为不超时" />
        </NFormItem>
        <NFormItem label="启用">
          <NCheckbox v-model:checked="editing.enabled">保存后立即启动</NCheckbox>
        </NFormItem>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton @click="showEditor = false">取消</NButton>
          <NButton type="primary" :loading="busy" @click="submitEditor">保存</NButton>
        </NSpace>
      </template>
    </NModal>
  </NSpace>
</template>

<style scoped>
.bridge-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.bridge-item {
  border: 1px solid rgba(128, 128, 128, 0.28);
  border-radius: 6px;
  padding: 10px 14px 10px;
}
.bridge-item.disabled {
  opacity: 0.75;
}
.bridge-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}
.bridge-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1 1 auto;
  min-width: 0;
}
.bridge-name {
  font-weight: 600;
  font-size: 15px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.bridge-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-left: auto;
}
.bridge-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 22px;
  margin-top: 8px;
  font-size: 13px;
  line-height: 22px;
}
.meta {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.meta .k {
  opacity: 0.6;
  font-size: 12px;
}
.meta .v {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.meta .sub {
  font-size: 12px;
}
.meta .lines {
  gap: 4px;
}
.bridge-err {
  display: block;
  margin-top: 6px;
  font-size: 12px;
}
.editor-modal {
  width: 640px;
  max-width: calc(100vw - 32px);
}
</style>
