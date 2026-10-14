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
import type { Peer, TailscaleSnapshot } from '../api'
import { useStatus } from '../status'

const { status } = useStatus()
const message = useMessage()

const snap = computed<TailscaleSnapshot | undefined>(() => status.value?.tailscale)
const isLocal = computed(() => status.value?.source === 'local')

const form = ref({ authKey: '', hostname: '', controlURL: '' })
const busy = ref(false)
const peers = ref<Peer[]>([])
let peerTimer: number | undefined

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
  { title: '节点', key: 'name', render: (r) => r.name.split('.')[0] },
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
]

onMounted(() => {
  form.value.hostname = snap.value?.hostname ?? ''
  form.value.controlURL = snap.value?.controlURL ?? ''
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
      <NAlert v-if="snap?.message" :type="snap.state === 'Running' ? 'info' : 'warning'" style="margin-bottom: 12px">{{ snap.message }}</NAlert>
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
        <NFormItem label=" ">
          <NButton type="primary" :loading="busy" @click="login">登录</NButton>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard v-if="snap?.state === 'Running'" title="对端">
      <NDataTable :columns="peerColumns" :data="peers" size="small" :bordered="false" :max-height="360" />
    </NCard>
  </NSpace>
</template>
