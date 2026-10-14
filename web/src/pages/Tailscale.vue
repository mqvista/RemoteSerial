<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NForm,
  NFormItem,
  NInput,
  NPopconfirm,
  NSpace,
  NTag,
  NText,
  useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api, formatBytes } from '../api'
import type { NetcheckReport, Peer, PingResult, TailscaleSnapshot } from '../api'
import { useStatus } from '../status'

const { status } = useStatus()
const message = useMessage()

const snap = computed<TailscaleSnapshot | undefined>(() => status.value?.tailscale)
const isLocal = computed(() => status.value?.source === 'local')

const form = ref({ authKey: '', hostname: '', controlURL: '', tags: '' })
const busy = ref(false)
const peers = ref<Peer[]>([])
let peerTimer: number | undefined

const netcheck = ref<NetcheckReport | null>(null)
const netcheckBusy = ref(false)
const pingBusy = ref<string | null>(null)
const pingResults = ref<Record<string, PingResult[]>>({})

async function runNetcheck() {
  netcheckBusy.value = true
  try {
    netcheck.value = await api.netcheck()
  } catch (e) {
    message.error(String(e))
  } finally {
    netcheckBusy.value = false
  }
}

async function pingPeer(ip: string) {
  pingBusy.value = ip
  try {
    pingResults.value = { ...pingResults.value, [ip]: (await api.ping(ip)).results }
  } catch (e) {
    message.error(String(e))
  } finally {
    pingBusy.value = null
  }
}

function pingSummary(ip: string): string {
  const rs = pingResults.value[ip]
  if (!rs) return ''
  return rs
    .map((r) => {
      if (!r.ok) return `${r.type}: ${r.err || '失败'}`
      const via = r.endpoint ? `直连 ${r.endpoint}` : r.peerRelay ? `peer relay ${r.peerRelay}` : r.derp ? `中转 ${r.derp}` : ''
      return `${r.type} ${r.latencyMs.toFixed(1)}ms ${via}`
    })
    .join(' · ')
}

function yesNo(v?: boolean) {
  if (v === undefined) return '未知'
  return v ? '是' : '否'
}

const stateType = computed(() => {
  switch (snap.value?.state) {
    case 'Running':
      return 'success'
    case 'Starting':
      return 'info'
    case 'NeedsLogin':
    case 'NeedsMachineAuth':
    case 'NotConfigured':
      return 'warning'
    case 'Error':
      return 'error'
    default:
      return 'default'
  }
})

const stateLabel = computed(() => {
  switch (snap.value?.state) {
    case 'NotConfigured':
      return '未登录'
    case 'Starting':
      return '连接中'
    case 'NeedsLogin':
      return '需要登录'
    case 'NeedsMachineAuth':
      return '等待批准'
    case 'Running':
      return '在线'
    case 'Stopped':
      return '已停止'
    case 'Error':
      return '错误'
    default:
      return snap.value?.state ?? ''
  }
})

const canLogin = computed(() => {
  const s = snap.value?.state
  return s === 'NotConfigured' || s === 'NeedsLogin' || s === 'Error'
})
const canStart = computed(() => snap.value?.state === 'Stopped')
const canStop = computed(() => snap.value?.state === 'Running' || snap.value?.state === 'Starting' || snap.value?.state === 'NeedsMachineAuth')

async function login() {
  if (!form.value.authKey.trim() && !snap.value?.hasState) {
    message.warning('请输入 auth key')
    return
  }
  busy.value = true
  try {
    await api.tailscaleLogin({
      authKey: form.value.authKey.trim(),
      hostname: form.value.hostname.trim(),
      controlURL: form.value.controlURL.trim(),
      tags: form.value.tags.split(/[,，;\s]+/).map((t) => t.trim()).filter(Boolean),
    })
    form.value.authKey = ''
    message.success('已发起登录')
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
  }
}

async function act(fn: () => Promise<unknown>, okMsg: string) {
  busy.value = true
  try {
    await fn()
    message.success(okMsg)
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
  }
}

async function loadPeers() {
  if (snap.value?.state !== 'Running') {
    peers.value = []
    return
  }
  try {
    peers.value = (await api.tailscalePeers()).peers.sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name))
  } catch {
    // 节点未运行时忽略
  }
}

const peerColumns: DataTableColumns<Peer> = [
  { title: '节点', key: 'name', minWidth: 140, ellipsis: { tooltip: true }, render: (r) => r.name.split('.')[0] },
  { title: 'IP', key: 'ip', width: 130 },
  { title: '系统', key: 'os', width: 90 },
  {
    title: '在线',
    key: 'online',
    width: 80,
    render: (r) => h(NTag, { size: 'small', type: r.online ? 'success' : 'default' }, { default: () => (r.online ? '在线' : '离线') }),
  },
  {
    title: '链路',
    key: 'direct',
    width: 160,
    render: (r) => {
      if (!r.online) return '-'
      if (r.direct) return h(NTag, { size: 'small', type: 'success' }, { default: () => `直连 ${r.curAddr}` })
      if (r.relay) return h(NTag, { size: 'small', type: 'warning' }, { default: () => `中转 ${r.relay}` })
      return h(NText, { depth: 3 }, { default: () => '未建立' })
    },
  },
  { title: '收/发', key: 'rx', width: 150, render: (r) => `${formatBytes(r.rxBytes)} / ${formatBytes(r.txBytes)}` },
  {
    title: '测路径',
    key: 'ping',
    minWidth: 220,
    render: (r) =>
      h('div', {}, [
        h(NButton, { size: 'tiny', disabled: !r.online || pingBusy.value === r.ip, loading: pingBusy.value === r.ip, onClick: () => pingPeer(r.ip) }, { default: () => 'ping' }),
        pingSummary(r.ip) ? h(NText, { depth: 3, style: 'margin-left:8px;font-size:12px' }, { default: () => pingSummary(r.ip) }) : null,
      ]),
  },
]

onMounted(() => {
  form.value.hostname = snap.value?.hostname ?? ''
  form.value.controlURL = snap.value?.controlURL ?? ''
  form.value.tags = (snap.value?.advertiseTags ?? []).join(', ')
  loadPeers()
  peerTimer = window.setInterval(loadPeers, 5000)
})
onUnmounted(() => {
  if (peerTimer) window.clearInterval(peerTimer)
})
</script>

<template>
  <NSpace vertical :size="16">
    <NCard title="节点状态">
      <template #header-extra>
        <NTag :type="stateType">{{ stateLabel }}</NTag>
      </template>
      <NAlert v-if="snap?.message" :type="snap.state === 'Running' ? 'info' : 'warning'" style="margin-bottom: 12px">
        {{ snap.message }}
        <template v-if="snap.authURL">
          <br />
          认证链接：<a :href="snap.authURL" target="_blank" rel="noopener">{{ snap.authURL }}</a>（需要用 tag 的 owner 账号登录；也可以在下方用 auth key 重新登录）
        </template>
      </NAlert>
      <NDescriptions :column="2" label-placement="left" bordered size="small">
        <NDescriptionsItem label="节点名">{{ snap?.hostname }}</NDescriptionsItem>
        <NDescriptionsItem label="MagicDNS">{{ snap?.dnsName || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="IPv4">{{ snap?.ip || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="IPv6">{{ snap?.ipv6 || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="Tailnet">{{ snap?.tailnet || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="归属">
          <template v-if="snap?.tags?.length">
            <NTag v-for="t in snap.tags" :key="t" size="small" style="margin-right: 4px">{{ t }}</NTag>
          </template>
          <template v-else>{{ snap?.loginName || '-' }}</template>
        </NDescriptionsItem>
        <NDescriptionsItem label="控制服务器">{{ snap?.controlURL || '官方' }}</NDescriptionsItem>
        <NDescriptionsItem label="密钥过期">
          <template v-if="snap?.keyExpiry">{{ new Date(snap.keyExpiry).toLocaleString('zh-CN') }}</template>
          <template v-else-if="snap?.state === 'Running'"><NText type="success">不过期</NText></template>
          <template v-else>-</template>
        </NDescriptionsItem>
      </NDescriptions>
      <NSpace style="margin-top: 12px">
        <NButton v-if="canStart" type="primary" :loading="busy" @click="act(api.tailscaleStart, '已启动')">启动</NButton>
        <NButton v-if="canStop && isLocal" :loading="busy" @click="act(api.tailscaleStop, '已停止')">停止</NButton>
        <NPopconfirm v-if="snap?.hasState && isLocal" @positive-click="act(api.tailscaleLogout, '已退出并清除状态')">
          <template #trigger>
            <NButton type="error" ghost :loading="busy">退出并重置</NButton>
          </template>
          将从 tailnet 注销此节点并删除本地状态，下次需要重新输入 auth key。
        </NPopconfirm>
        <NText v-if="!isLocal" depth="3">停止与退出只能在远端本机操作，避免切断你自己的连接。</NText>
      </NSpace>
    </NCard>

    <NCard v-if="canLogin" title="登录">
      <NAlert type="info" style="margin-bottom: 12px">
        请使用 reusable、pre-authorized 且带 tag 的 auth key。带 tag 的节点不会因密钥过期掉线。auth key 只用于本次入网，不会保存。
        也可填 OAuth client secret（tskey-client-…），此时必须填 tag，适合批量部署，secret 不会过期。
      </NAlert>
      <NForm label-placement="left" label-width="110">
        <NFormItem label="Auth key">
          <NInput v-model:value="form.authKey" type="password" show-password-on="click" placeholder="tskey-auth-..." />
        </NFormItem>
        <NFormItem label="节点名">
          <NInput v-model:value="form.hostname" placeholder="rs-站点名" />
        </NFormItem>
        <NFormItem label="控制服务器">
          <NInput v-model:value="form.controlURL" placeholder="留空使用官方；Headscale 填 https://..." />
        </NFormItem>
        <NFormItem label="Tag">
          <NInput v-model:value="form.tags" placeholder="如 tag:serial-agent，多个用逗号分隔；普通 auth key 可留空" />
        </NFormItem>
        <NFormItem v-if="snap?.hasState" label=" ">
          <NText depth="3" style="font-size: 12px">节点已有登录状态，用 key 重新登录会保留节点身份；"退出并重置"才会生成新节点。</NText>
        </NFormItem>
        <NFormItem label=" ">
          <NButton type="primary" :loading="busy" @click="login">登录</NButton>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard v-if="snap?.state === 'Running'" title="对端">
      <template #header-extra>
        <NText depth="3" style="font-size: 12px">本节点首选 DERP：{{ snap?.homeRelay || '-' }}</NText>
      </template>
      <NDataTable :columns="peerColumns" :data="peers" size="small" :bordered="false" :max-height="360" :scroll-x="960" />
    </NCard>

    <NCard v-if="snap?.state === 'Running'" title="网络诊断">
      <template #header-extra>
        <NButton size="small" :loading="netcheckBusy" @click="runNetcheck">运行 netcheck</NButton>
      </template>
      <NText v-if="!netcheck" depth="3">检测 UDP 可达性、NAT 类型、端口映射和到各 DERP 的延迟，耗时约 5 到 10 秒。</NText>
      <template v-else>
        <NDescriptions :column="3" label-placement="left" size="small" bordered>
          <NDescriptionsItem label="UDP">
            <NTag size="small" :type="netcheck.udp ? 'success' : 'error'">{{ netcheck.udp ? '可用' : '不可用' }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem label="困难 NAT">
            <NTag size="small" :type="netcheck.hardNAT ? 'warning' : 'success'">{{ yesNo(netcheck.hardNAT) }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem label="IPv4 / IPv6">{{ yesNo(netcheck.ipv4) }} / {{ yesNo(netcheck.ipv6) }}</NDescriptionsItem>
          <NDescriptionsItem label="UPnP / PMP / PCP">{{ yesNo(netcheck.upnp) }} / {{ yesNo(netcheck.pmp) }} / {{ yesNo(netcheck.pcp) }}</NDescriptionsItem>
          <NDescriptionsItem label="公网出口">{{ netcheck.globalV4 || '-' }}</NDescriptionsItem>
          <NDescriptionsItem label="首选 DERP">{{ netcheck.preferredDERP || '-' }}</NDescriptionsItem>
        </NDescriptions>
        <NText v-if="netcheck.hardNAT" type="warning" style="display: block; margin-top: 8px; font-size: 12px">
          映射随目标变化（对称 NAT），与同样处于困难 NAT 后的设备很难直连，会走中转。可在自有服务器上部署 peer relay 或 DERP 改善。
        </NText>
        <NDataTable
          style="margin-top: 12px"
          :columns="[
            { title: 'DERP 区域', key: 'name', render: (r: any) => `${r.name} (${r.code})` },
            { title: '延迟', key: 'latencyMs', width: 120, render: (r: any) => `${r.latencyMs.toFixed(1)} ms` },
            { title: '', key: 'preferred', width: 80, render: (r: any) => (r.preferred ? h(NTag, { size: 'small', type: 'success' }, { default: () => '首选' }) : '') },
          ]"
          :data="netcheck.regions"
          size="small"
          :bordered="false"
        />
        <NText depth="3" style="font-size: 12px">耗时 {{ netcheck.tookMs }} ms</NText>
      </template>
    </NCard>
  </NSpace>
</template>
