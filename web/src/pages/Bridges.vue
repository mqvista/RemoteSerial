<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NDataTable,
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
  { title: '文件', key: 'name' },
  { title: '大小', key: 'size', width: 110, render: (f) => formatBytes(f.size) },
  { title: '时间', key: 'modTime', width: 170, render: (f) => new Date(f.modTime).toLocaleString('zh-CN', { hour12: false }) },
  {
    title: '操作',
    key: 'actions',
    width: 160,
    render: (f) =>
      h(NSpace, { size: 4 }, {
        default: () => [
          h('a', { href: `/api/captures/${encodeURIComponent(f.name)}`, download: f.name, style: 'font-size:12px' }, '下载'),
          h(NPopconfirm, { onPositiveClick: () => deleteCapture(f.name) }, { trigger: () => h(NButton, { size: 'tiny', type: 'error', ghost: true }, { default: () => '删除' }), default: () => '删除此文件？' }),
        ],
      }),
  },
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

function stateTag(s?: BridgeStatus) {
  if (!s) return h(NTag, { size: 'small' }, { default: () => '未知' })
  const map: Record<string, [string, 'default' | 'info' | 'success' | 'warning' | 'error']> = {
    Disabled: ['已停用', 'default'],
    WaitingNetwork: ['等待节点上线', 'warning'],
    WaitingDevice: ['等待设备', 'warning'],
    Listening: ['监听中', 'info'],
    Connected: ['已连接', 'success'],
    Error: ['错误', 'error'],
  }
  const [label, type] = map[s.state] ?? [s.state, 'default']
  return h(NTag, { size: 'small', type }, { default: () => label })
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

const columns: DataTableColumns<BridgeConfig> = [
  { title: '名称', key: 'name', render: (c) => c.name || c.id },
  {
    title: '串口',
    key: 'port',
    render: (c) => {
      const s = statusById.value[c.id]
      const name = s?.resolvedPort || c.port || '(按 USB 匹配)'
      return h('span', {}, [name, c.match ? h(NText, { depth: 3, style: 'margin-left:6px;font-size:12px' }, { default: () => 'USB 匹配' }) : null])
    },
  },
  {
    title: '参数',
    key: 'params',
    render: (c) => {
      const s = statusById.value[c.id]
      const cfg = `${c.baud} ${c.dataBits}${c.parity}${c.stopBits}`
      if (!s || s.paramsBy !== 'client') return cfg
      return h('span', {}, [s.active, h(NTag, { size: 'tiny', type: 'info', style: 'margin-left:6px' }, { default: () => '客户端设置' })])
    },
  },
  {
    title: '控制线',
    key: 'lines',
    width: 110,
    render: (c) => {
      const s = statusById.value[c.id]
      if (!s?.deviceOpen) return '-'
      return h(NSpace, { size: 2 }, {
        default: () => [
          h(NTag, { size: 'tiny', type: s.dtr ? 'success' : 'default' }, { default: () => 'DTR' }),
          h(NTag, { size: 'tiny', type: s.rts ? 'success' : 'default' }, { default: () => 'RTS' }),
        ],
      })
    },
  },
  { title: '模式', key: 'mode', width: 90, render: (c) => (c.mode === 'rfc2217' ? 'RFC2217' : '裸 TCP') },
  { title: 'TCP 端口', key: 'tcpPort', width: 110, render: (c) => (c.localPort ? `${c.tcpPort} / 本机 ${c.localPort}` : String(c.tcpPort)) },
  { title: '状态', key: 'state', width: 120, render: (c) => stateTag(statusById.value[c.id]) },
  {
    title: '客户端',
    key: 'client',
    render: (c) => statusById.value[c.id]?.client || h(NText, { depth: 3 }, { default: () => '-' }),
  },
  {
    title: '设备→TCP / TCP→设备',
    key: 'bytes',
    width: 170,
    render: (c) => {
      const s = statusById.value[c.id]
      return s ? `${formatBytes(s.fromDevice)} / ${formatBytes(s.toDevice)}` : '-'
    },
  },
  {
    title: '操作',
    key: 'actions',
    width: 330,
    render: (c) => {
      const s = statusById.value[c.id]
      return h(NSpace, { size: 4 }, {
        default: () => [
          c.enabled
            ? h(NButton, { size: 'tiny', onClick: () => action(c.id, 'stop') }, { default: () => '停止' })
            : h(NButton, { size: 'tiny', type: 'primary', onClick: () => action(c.id, 'start') }, { default: () => '启动' }),
          h(NButton, { size: 'tiny', disabled: !s?.client, onClick: () => action(c.id, 'kick') }, { default: () => '踢掉' }),
          h(NButton, { size: 'tiny', type: s?.capture?.active ? 'warning' : 'default', disabled: !c.enabled, onClick: () => toggleCapture(c.id) }, { default: () => (s?.capture?.active ? '停止录制' : '录制') }),
          h(NButton, { size: 'tiny', onClick: () => openEdit(c) }, { default: () => '编辑' }),
          h(NPopconfirm, { onPositiveClick: () => remove(c.id) }, { trigger: () => h(NButton, { size: 'tiny', type: 'error', ghost: true }, { default: () => '删除' }), default: () => '删除此桥接？' }),
        ],
      })
    },
  },
]

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
        <NSpace>
          <NButton size="small" @click="loadPorts">刷新串口</NButton>
          <NButton size="small" type="primary" @click="openNew">新增桥接</NButton>
        </NSpace>
      </template>
      <NDataTable :columns="columns" :data="configs" :row-key="(r: BridgeConfig) => r.id" size="small" :bordered="false" />
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
      <NDataTable :columns="captureColumns" :data="captures" size="small" :bordered="false" />
    </NCard>

    <NCard title="本机串口">
      <NDataTable
        :columns="[
          { title: '串口', key: 'name', width: 100 },
          { title: '类型', key: 'isUSB', width: 80, render: (p: SerialPort) => (p.isUSB ? 'USB' : '板载') },
          { title: '芯片', key: 'chip', render: (p: SerialPort) => p.chip || '-' },
          { title: 'VID:PID', key: 'vidpid', width: 110, render: (p: SerialPort) => (p.vid ? `${p.vid}:${p.pid}` : '-') },
          { title: '序列号', key: 'serialNumber', render: (p: SerialPort) => p.serialNumber || '-' },
          { title: '占用', key: 'usedBy', width: 100, render: (p: SerialPort) => p.usedBy || '-' },
        ]"
        :data="ports"
        size="small"
        :bordered="false"
      />
    </NCard>

    <NModal v-model:show="showEditor" preset="card" :title="editingIsNew ? '新增桥接' : '编辑桥接'" style="width: 640px">
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
