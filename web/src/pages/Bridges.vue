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
import { api, BAUD_RATES, formatBytes, formatRate } from '../api'
import type { BridgeConfig, BridgeStatus, CaptureFile, SerialPort } from '../api'
import { useStatus } from '../status'
import { formatTime, t, tOr } from '../i18n'

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
      message.success(t('bridges.recordingStopped'))
    } else {
      const r = await api.captureStart(id)
      message.success(t('bridges.recordingStarted', { base: r.base }))
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
    message.success(t('common.deleted'))
  } catch (e) {
    message.error(String(e))
  } finally {
    loadCaptures()
  }
}

const captureColumns = computed<DataTableColumns<CaptureFile>>(() => [
  { title: t('captures.file'), key: 'name', minWidth: 220, ellipsis: { tooltip: true } },
  { title: t('captures.size'), key: 'size', width: 100, render: (f) => formatBytes(f.size) },
  { title: t('captures.time'), key: 'modTime', width: 180, render: (f) => formatTime(f.modTime) },
  {
    title: t('captures.actions'),
    key: 'actions',
    width: 130,
    render: (f) =>
      h(NSpace, { size: 4, wrap: false }, {
        default: () => [
          h('a', { href: `/api/captures/${encodeURIComponent(f.name)}`, download: f.name, style: 'font-size:12px' }, t('common.download')),
          h(NPopconfirm, { onPositiveClick: () => deleteCapture(f.name) }, { trigger: () => h(NButton, { size: 'tiny', type: 'error', ghost: true }, { default: () => t('common.delete') }), default: () => t('captures.deleteConfirm') }),
        ],
      }),
  },
])

// 串口名在 macOS/Linux 上很长（/dev/cu.usbserial-XXXX），列宽要够，超出时省略并悬停显示全名，
// 否则全局 nowrap 会让文字溢出盖住相邻列。
const portColumns = computed<DataTableColumns<SerialPort>>(() => [
  { title: t('ports.port'), key: 'name', width: 300, ellipsis: { tooltip: true }, className: 'mono' },
  { title: t('ports.type'), key: 'isUSB', width: 90, render: (p) => (p.isUSB ? 'USB' : t('ports.typeOther')) },
  { title: t('ports.chip'), key: 'chip', minWidth: 140, ellipsis: { tooltip: true }, render: (p) => p.chip || '-' },
  { title: 'VID:PID', key: 'vidpid', width: 110, render: (p) => (p.vid ? `${p.vid}:${p.pid}` : '-') },
  { title: t('ports.serial'), key: 'serialNumber', minWidth: 130, ellipsis: { tooltip: true }, render: (p) => p.serialNumber || '-' },
  { title: t('ports.usedBy'), key: 'usedBy', minWidth: 110, ellipsis: { tooltip: true }, render: (p) => p.usedBy || '-' },
])

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

const portsLoading = ref(false)

async function loadPorts() {
  portsLoading.value = true
  try {
    ports.value = (await api.serialPorts()).ports
    // 新增时若还没选端口（打开对话框时列表为空），自动选第一个
    if (editingIsNew.value && editing.value && !editing.value.port && ports.value.length) {
      editing.value.port = ports.value[0].name
    }
  } catch (e) {
    message.error(String(e))
  } finally {
    portsLoading.value = false
  }
}

// 每次展开串口下拉框都重新检测，插拔后不用关掉对话框
function onPortMenu(show: boolean) {
  if (show) loadPorts()
}

const portOptions = computed<SelectOption[]>(() =>
  ports.value.map((p) => {
    const extra = [p.chip, p.vid && p.pid ? `${p.vid}:${p.pid}` : '', p.serialNumber ? `SN ${p.serialNumber}` : '', p.usedBy ? t('ports.optUsedBy', { by: p.usedBy }) : '']
      .filter(Boolean)
      .join(' · ')
    return { label: extra ? `${p.name}  (${extra})` : p.name, value: p.name }
  }),
)

const baudOptions = BAUD_RATES.map((b) => ({ label: String(b), value: b }))
const parityOptions = computed(() => [
  { label: t('parity.N'), value: 'N' },
  { label: t('parity.E'), value: 'E' },
  { label: t('parity.O'), value: 'O' },
  { label: 'Mark (M)', value: 'M' },
  { label: 'Space (S)', value: 'S' },
])
const stopOptions = [
  { label: '1', value: 1 },
  { label: '1.5', value: 1.5 },
  { label: '2', value: 2 },
]
const modeOptions = computed(() => [
  { label: t('mode.raw'), value: 'raw' },
  { label: t('mode.rfc2217'), value: 'rfc2217' },
])
const policyOptions = computed(() => [
  { label: t('policy.replace'), value: 'replace' },
  { label: t('policy.reject'), value: 'reject' },
])

type TagType = 'default' | 'info' | 'success' | 'warning' | 'error'
const stateTypes: Record<string, TagType> = {
  Disabled: 'default',
  WaitingNetwork: 'warning',
  WaitingDevice: 'warning',
  Listening: 'info',
  Connected: 'success',
  Error: 'error',
}
function stateInfo(s?: BridgeStatus): { label: string; type: TagType } {
  if (!s) return { label: t('common.unknown'), type: 'default' }
  return { label: tOr(`bridgeState.${s.state}`, s.state), type: stateTypes[s.state] ?? 'default' }
}

function paramsText(c: BridgeConfig): string {
  const s = statusById.value[c.id]
  if (s && s.paramsBy === 'client' && s.active) return s.active
  return `${c.baud} ${c.dataBits}${c.parity}${c.stopBits}`
}

function clientSince(s?: BridgeStatus): string {
  if (!s?.clientSince) return ''
  const sec = Math.max(0, Math.floor((Date.now() - new Date(s.clientSince).getTime()) / 1000))
  if (sec < 60) return t('time.sec', { n: sec })
  if (sec < 3600) return t('time.min', { n: Math.floor(sec / 60) })
  return t('time.hm', { h: Math.floor(sec / 3600), m: Math.floor((sec % 3600) / 60) })
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
    disconnectOnUnplug: false,
    openOnConnect: false,
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

// 换了串口时，USB 比对跟着换成新串口的识别码（否则会一直比对最初选的那个装置）
function onPortChange(name: string) {
  if (!editing.value) return
  editing.value.port = name
  if (!matchByUSB.value) return
  const p = ports.value.find((x) => x.name === name)
  if (p?.isUSB) editing.value.match = { vid: p.vid ?? '', pid: p.pid ?? '', serial: p.serialNumber ?? '' }
}

// sameMatch 判断两个 USB 比对是否会比对到同一个装置
function sameMatch(a: BridgeConfig['match'], b: BridgeConfig['match']): boolean {
  if (!a || !b || !a.serial || !b.serial) return false
  const up = (s: string) => s.trim().toUpperCase()
  return up(a.vid) === up(b.vid) && up(a.pid) === up(b.pid) && a.serial.trim() === b.serial.trim()
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
    message.warning(t('bridges.selectPort'))
    return
  }
  const list = configs.value.filter((c) => c.id !== e.id)
  const dup = list.find((c) => sameMatch(c.match, e.match))
  if (dup) {
    message.error(t('editor.dupMatch', { name: dup.name || dup.id }))
    return
  }
  list.push(e)
  list.sort((a, b) => a.tcpPort - b.tcpPort)
  if (await saveAll(list, editingIsNew.value ? t('common.added') : t('common.saved'))) showEditor.value = false
}

async function remove(id: string) {
  await saveAll(
    configs.value.filter((c) => c.id !== id),
    t('common.deleted'),
  )
}

async function action(id: string, act: 'start' | 'stop' | 'kick') {
  busy.value = true
  try {
    const r = await api.bridgeAction(id, act)
    configs.value = r.bridges.map((b) => b.config)
    if (r.warning) message.warning(r.warning)
    else message.success({ start: t('common.started'), stop: t('common.stopped'), kick: t('bridges.kicked') }[act])
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
    <NAlert v-if="!nodeRunning" type="warning">{{ t('bridges.nodeOffline') }}</NAlert>
    <NCard :title="t('bridges.title')">
      <template #header-extra>
        <NSpace :size="8">
          <NButton size="small" @click="loadPorts">{{ t('bridges.refreshPorts') }}</NButton>
          <NButton size="small" type="primary" @click="openNew">{{ t('bridges.new') }}</NButton>
        </NSpace>
      </template>

      <NEmpty v-if="!configs.length" :description="t('bridges.empty')" />
      <div v-else class="bridge-list">
        <div v-for="c in configs" :key="c.id" class="bridge-item" :class="{ disabled: !c.enabled }">
          <div class="bridge-head">
            <div class="bridge-title">
              <span class="bridge-name" :title="c.name || c.id">{{ c.name || c.id }}</span>
              <NTag size="small" :type="stateInfo(statusById[c.id]).type" :bordered="false">{{ stateInfo(statusById[c.id]).label }}</NTag>
              <NTag size="small" :bordered="false">{{ c.mode === 'rfc2217' ? 'RFC2217' : t('bridges.raw') }}</NTag>
              <NTag v-if="statusById[c.id]?.capture?.active" size="small" type="warning" :bordered="false">{{ t('bridges.recording') }}</NTag>
            </div>
            <div class="bridge-actions">
              <NButton v-if="c.enabled" size="tiny" @click="action(c.id, 'stop')">{{ t('common.stop') }}</NButton>
              <NButton v-else size="tiny" type="primary" @click="action(c.id, 'start')">{{ t('common.start') }}</NButton>
              <NButton size="tiny" :disabled="!c.enabled" @click="openTerminal(c.id)">{{ t('bridges.terminal') }}</NButton>
              <NButton size="tiny" :disabled="!statusById[c.id]?.client" @click="action(c.id, 'kick')">{{ t('bridges.kick') }}</NButton>
              <NButton size="tiny" :type="statusById[c.id]?.capture?.active ? 'warning' : 'default'" :disabled="!c.enabled" @click="toggleCapture(c.id)">
                {{ statusById[c.id]?.capture?.active ? t('bridges.stopRecord') : t('bridges.record') }}
              </NButton>
              <NButton size="tiny" @click="openEdit(c)">{{ t('common.edit') }}</NButton>
              <NPopconfirm @positive-click="remove(c.id)">
                <template #trigger>
                  <NButton size="tiny" type="error" ghost>{{ t('common.delete') }}</NButton>
                </template>
                {{ t('bridges.deleteConfirm') }}
              </NPopconfirm>
            </div>
          </div>

          <div class="bridge-meta">
            <div class="meta">
              <span class="k">{{ t('bridges.metaPort') }}</span>
              <span class="v">
                <span class="clip mono" :title="statusById[c.id]?.resolvedPort || c.port">{{ statusById[c.id]?.resolvedPort || c.port || t('bridges.byUSB') }}</span>
                <NText v-if="c.match" depth="3" class="sub">{{ t('bridges.usbMatch') }}</NText>
              </span>
            </div>
            <div class="meta">
              <span class="k">{{ t('bridges.metaParams') }}</span>
              <span class="v">
                {{ paramsText(c) }}
                <NTag v-if="statusById[c.id]?.paramsBy === 'client'" size="tiny" type="info" :bordered="false">{{ t('bridges.clientSet') }}</NTag>
              </span>
            </div>
            <div class="meta">
              <span class="k">{{ t('bridges.metaTCP') }}</span>
              <span class="v">
                {{ c.tcpPort }}
                <NText v-if="c.localPort" depth="3" class="sub">{{ t('bridges.localPort', { n: c.localPort }) }}</NText>
              </span>
            </div>
            <div class="meta">
              <span class="k">{{ t('bridges.metaClient') }}</span>
              <span class="v">
                <template v-if="statusById[c.id]?.client">
                  <span class="clip" :title="statusById[c.id].client">{{ statusById[c.id].client }}</span>
                  <NText depth="3" class="sub">{{ clientSince(statusById[c.id]) }}</NText>
                </template>
                <NText v-else depth="3">{{ t('common.none') }}</NText>
                <NText v-if="statusById[c.id]?.viewers" depth="3" class="sub">{{ t('bridges.viewers', { n: statusById[c.id].viewers }) }}</NText>
              </span>
            </div>
            <div class="meta">
              <span class="k">{{ t('bridges.metaTraffic') }}</span>
              <span class="v" :title="t('bridges.trafficTip')">
                <template v-if="statusById[c.id]">
                  ↓ {{ formatBytes(statusById[c.id].fromDevice) }} ↑ {{ formatBytes(statusById[c.id].toDevice) }}
                  <NText depth="3" class="sub" :title="t('bridges.rateTip')">
                    ↓ {{ formatRate(statusById[c.id].rateFromDevice) }} ↑ {{ formatRate(statusById[c.id].rateToDevice) }}
                  </NText>
                </template>
                <template v-else>-</template>
              </span>
            </div>
            <div v-if="statusById[c.id]?.deviceOpen" class="meta">
              <span class="k">{{ t('bridges.metaLines') }}</span>
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
          {{ t('bridges.example1') }} <code>nc {{ status?.tailscale.ip || '100.x.y.z' }} {{ configs[0].tcpPort }}</code> {{ t('bridges.example2') }}
          <code>rfc2217://{{ status?.tailscale.ip || '100.x.y.z' }}:{{ configs[0].tcpPort }}</code> {{ t('bridges.example3') }}
        </NText>
      </template>
    </NCard>

    <NCard :title="t('captures.title')">
      <template #header-extra>
        <NButton size="small" @click="loadCaptures">{{ t('common.refresh') }}</NButton>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 8px; font-size: 12px">
        {{ t('captures.hint') }}
      </NText>
      <NDataTable :columns="captureColumns" :data="captures" size="small" :bordered="false" :scroll-x="640" />
    </NCard>

    <NCard :title="t('ports.title')">
      <template #header-extra>
        <NButton size="small" @click="loadPorts">{{ t('common.refresh') }}</NButton>
      </template>
      <NDataTable :columns="portColumns" :data="ports" size="small" :bordered="false" :scroll-x="880" table-layout="fixed">
        <template #empty><NEmpty :description="t('ports.empty')" /></template>
      </NDataTable>
    </NCard>

    <NModal v-model:show="showEditor" preset="card" :title="editingIsNew ? t('editor.new') : t('editor.edit')" class="editor-modal">
      <NForm v-if="editing" label-placement="left" label-width="auto">
        <NFormItem :label="t('editor.name')">
          <NInput v-model:value="editing.name" :placeholder="t('editor.namePh')" />
        </NFormItem>
        <NFormItem :label="t('editor.port')">
          <NSelect :value="editing.port" :options="portOptions" @update:value="onPortChange" :loading="portsLoading" filterable tag @update:show="onPortMenu" :placeholder="t('editor.portPh')" />
        </NFormItem>
        <NFormItem :label="t('editor.usb')">
          <NSpace vertical :size="4">
            <NCheckbox :checked="matchByUSB" @update:checked="onMatchToggle">{{ t('editor.usbHint') }}</NCheckbox>
            <NSpace v-if="matchByUSB && editing.match">
              <NInput v-model:value="editing.match.vid" placeholder="VID" style="width: 100px" />
              <NInput v-model:value="editing.match.pid" placeholder="PID" style="width: 100px" />
              <NInput v-model:value="editing.match.serial" :placeholder="t('editor.serialPh')" style="width: 220px" />
            </NSpace>
          </NSpace>
        </NFormItem>
        <NFormItem :label="t('editor.baud')">
          <NSelect v-model:value="editing.baud" :options="baudOptions" filterable tag />
        </NFormItem>
        <NFormItem :label="t('editor.frame')">
          <NSpace>
            <NInputNumber v-model:value="editing.dataBits" :min="5" :max="8" style="width: 90px" />
            <NSelect v-model:value="editing.parity" :options="parityOptions" style="width: 130px" />
            <NSelect v-model:value="editing.stopBits" :options="stopOptions" style="width: 90px" />
          </NSpace>
        </NFormItem>
        <NFormItem :label="t('editor.mode')">
          <NSelect v-model:value="editing.mode" :options="modeOptions" />
        </NFormItem>
        <NFormItem :label="t('editor.tcpPort')">
          <NInputNumber v-model:value="editing.tcpPort" :min="1" :max="65535" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">{{ t('editor.tcpHint') }}</NText>
        </NFormItem>
        <NFormItem :label="t('editor.localPort')">
          <NInputNumber v-model:value="editing.localPort" :min="0" :max="65535" :placeholder="t('editor.localPh')" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">{{ t('editor.localHint') }}</NText>
        </NFormItem>
        <NFormItem :label="t('editor.policy')">
          <NSelect v-model:value="editing.clientPolicy" :options="policyOptions" />
        </NFormItem>
        <NFormItem :label="t('editor.idle')">
          <NInputNumber v-model:value="editing.idleTimeoutSec" :min="0" :placeholder="t('editor.idlePh')" />
        </NFormItem>
        <NFormItem :label="t('editor.unplug')">
          <NCheckbox v-model:checked="editing.disconnectOnUnplug">{{ t('editor.unplugHint') }}</NCheckbox>
        </NFormItem>
        <NFormItem :label="t('editor.onConnect')">
          <NCheckbox v-model:checked="editing.openOnConnect">{{ t('editor.onConnectHint') }}</NCheckbox>
        </NFormItem>
        <NFormItem :label="t('editor.enabled')">
          <NCheckbox v-model:checked="editing.enabled">{{ t('editor.enabledHint') }}</NCheckbox>
        </NFormItem>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton @click="showEditor = false">{{ t('common.cancel') }}</NButton>
          <NButton type="primary" :loading="busy" @click="submitEditor">{{ t('common.save') }}</NButton>
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
  max-width: 100%;
  min-width: 0;
}
.meta .k {
  opacity: 0.6;
  font-size: 12px;
}
.meta .v {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.meta .clip {
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
}
.mono,
:deep(td.mono) {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
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
