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
  NQrCode,
  NSpace,
  NTag,
  NText,
  useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api, formatBytes } from '../api'
import type { NetcheckReport, Peer, PingResult, SystemAction, SystemJob, TailscaleSnapshot } from '../api'
import { useStatus } from '../status'
import { formatTime, t, tOr } from '../i18n'

const { status } = useStatus()
const message = useMessage()

const snap = computed<TailscaleSnapshot | undefined>(() => status.value?.tailscale)
const isLocal = computed(() => status.value?.source === 'local')

// ---- 系统 Tailscale（OpenWrt 默认） ----
const isSystem = computed(() => snap.value?.backend === 'system')
const sys = computed(() => snap.value?.system)
// 内嵌与系统 Tailscale 之间切换：只在 Linux / OpenWrt 上有意义（其他系统的 Tailscale 客户端不提供这个 socket）
const showBackendSwitch = computed(() => isLocal.value && (isSystem.value || status.value?.platform === 'linux' || status.value?.platform === 'openwrt'))
const sysStep = computed(() => {
  const s = snap.value?.state
  if (s === 'NotInstalled') return 'install'
  if (s === 'NotRunning') return sys.value?.servicePackage ? 'service' : 'start'
  if (sys.value?.firewallZone === false) return 'firewall'
  return ''
})
const job = ref<SystemJob | null>(null)
let jobTimer: number | undefined

async function pollJob() {
  try {
    job.value = await api.systemJob()
  } catch {
    // 忽略
  }
  if (job.value?.running) jobTimer = window.setTimeout(pollJob, 1000)
}

async function runSystem(action: SystemAction) {
  try {
    job.value = await api.systemAction(action)
    window.clearTimeout(jobTimer)
    jobTimer = window.setTimeout(pollJob, 1000)
  } catch (e) {
    message.error(String(e))
  }
}

async function switchBackend(b: 'embedded' | 'system') {
  busy.value = true
  try {
    await api.setBackend(b)
    message.success(t('tsp.switched'))
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
  }
}

async function loginBrowser() {
  busy.value = true
  try {
    await api.tailscaleLoginInteractive()
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
  }
}

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
      if (!r.ok) return `${r.type}: ${r.err || t('tsp.pingFailed')}`
      const via = r.endpoint ? t('tsp.direct', { a: r.endpoint }) : r.peerRelay ? `peer relay ${r.peerRelay}` : r.derp ? t('tsp.relay', { r: r.derp }) : ''
      return `${r.type} ${r.latencyMs.toFixed(1)}ms ${via}`
    })
    .join(' · ')
}

function yesNo(v?: boolean) {
  if (v === undefined) return t('common.unknown')
  return v ? t('common.yes') : t('common.no')
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
    case 'NotInstalled':
    case 'NotRunning':
      return 'warning'
    case 'Error':
      return 'error'
    default:
      return 'default'
  }
})

const stateLabel = computed(() => {
  const s = snap.value?.state ?? ''
  return s ? tOr(`ts.${s}`, s) : ''
})

const canLogin = computed(() => {
  const s = snap.value?.state
  if (isSystem.value) return s === 'NeedsLogin'
  return s === 'NotConfigured' || s === 'NeedsLogin' || s === 'Error'
})
const canStart = computed(() => snap.value?.state === 'Stopped')
const canStop = computed(() => !isSystem.value && (snap.value?.state === 'Running' || snap.value?.state === 'Starting' || snap.value?.state === 'NeedsMachineAuth'))

async function login() {
  if (!form.value.authKey.trim() && !snap.value?.hasState) {
    message.warning(t('tsp.enterKey'))
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
    message.success(t('tsp.loginStarted'))
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

const peerColumns = computed<DataTableColumns<Peer>>(() => [
  { title: t('tsp.peer'), key: 'name', minWidth: 140, ellipsis: { tooltip: true }, render: (r) => r.name.split('.')[0] },
  { title: 'IP', key: 'ip', width: 130 },
  { title: t('tsp.os'), key: 'os', width: 90 },
  {
    title: t('tsp.online'),
    key: 'online',
    width: 90,
    render: (r) => h(NTag, { size: 'small', type: r.online ? 'success' : 'default' }, { default: () => (r.online ? t('tsp.online') : t('tsp.offline')) }),
  },
  {
    title: t('tsp.path'),
    key: 'direct',
    width: 180,
    render: (r) => {
      if (!r.online) return '-'
      if (r.direct) return h(NTag, { size: 'small', type: 'success' }, { default: () => t('tsp.direct', { a: r.curAddr ?? '' }) })
      if (r.relay) return h(NTag, { size: 'small', type: 'warning' }, { default: () => t('tsp.relay', { r: r.relay ?? '' }) })
      return h(NText, { depth: 3 }, { default: () => t('tsp.notEstablished') })
    },
  },
  { title: t('tsp.rxtx'), key: 'rx', width: 150, render: (r) => `${formatBytes(r.rxBytes)} / ${formatBytes(r.txBytes)}` },
  {
    title: t('tsp.testPath'),
    key: 'ping',
    minWidth: 220,
    render: (r) =>
      h('div', {}, [
        h(NButton, { size: 'tiny', disabled: !r.online || pingBusy.value === r.ip, loading: pingBusy.value === r.ip, onClick: () => pingPeer(r.ip) }, { default: () => 'ping' }),
        pingSummary(r.ip) ? h(NText, { depth: 3, style: 'margin-left:8px;font-size:12px' }, { default: () => pingSummary(r.ip) }) : null,
      ]),
  },
])

const derpColumns = computed(() => [
  { title: t('tsp.derpRegion'), key: 'name', render: (r: any) => `${r.name} (${r.code})` },
  { title: t('tsp.latency'), key: 'latencyMs', width: 120, render: (r: any) => `${r.latencyMs.toFixed(1)} ms` },
  { title: '', key: 'preferred', width: 90, render: (r: any) => (r.preferred ? h(NTag, { size: 'small', type: 'success' }, { default: () => t('tsp.preferred') }) : '') },
])

onMounted(() => {
  form.value.hostname = snap.value?.hostname ?? ''
  form.value.controlURL = snap.value?.controlURL ?? ''
  form.value.tags = (snap.value?.advertiseTags ?? []).join(', ')
  loadPeers()
  peerTimer = window.setInterval(loadPeers, 5000)
  if (isSystem.value) pollJob()
})
onUnmounted(() => {
  if (peerTimer) window.clearInterval(peerTimer)
  window.clearTimeout(jobTimer)
})
</script>

<template>
  <NSpace vertical :size="16">
    <NCard :title="t('tsp.nodeStatus')">
      <template #header-extra>
        <NTag :type="stateType">{{ stateLabel }}</NTag>
      </template>
      <NAlert v-if="snap?.message" :type="snap.state === 'Running' ? 'info' : 'warning'" style="margin-bottom: 12px">
        {{ snap.message }}
        <template v-if="snap.authURL">
          <br />
          {{ t('tsp.authLink') }} <a :href="snap.authURL" target="_blank" rel="noopener">{{ snap.authURL }}</a> {{ t('tsp.authLinkHint') }}
        </template>
      </NAlert>
      <NDescriptions :column="2" label-placement="left" bordered size="small">
        <NDescriptionsItem :label="t('tsp.hostname')">{{ snap?.hostname || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="MagicDNS">{{ snap?.dnsName || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="IPv4">{{ snap?.ip || '-' }}</NDescriptionsItem>
        <NDescriptionsItem label="IPv6">{{ snap?.ipv6 || '-' }}</NDescriptionsItem>
        <NDescriptionsItem :label="t('tsp.tailnetName')">{{ snap?.tailnet || '-' }}</NDescriptionsItem>
        <NDescriptionsItem :label="t('tsp.owner')">
          <template v-if="snap?.tags?.length">
            <NTag v-for="t in snap.tags" :key="t" size="small" style="margin-right: 4px">{{ t }}</NTag>
          </template>
          <template v-else>{{ snap?.loginName || '-' }}</template>
        </NDescriptionsItem>
        <NDescriptionsItem :label="t('tsp.control')">{{ snap?.controlURL || t('tsp.official') }}</NDescriptionsItem>
        <NDescriptionsItem :label="t('tsp.backend')">
          {{ isSystem ? t('tsp.backendSystem') : t('tsp.backendEmbedded') }}
          <NText v-if="sys?.version" depth="3"> · {{ sys.version }}</NText>
        </NDescriptionsItem>
        <NDescriptionsItem :label="t('tsp.keyExpiry')">
          <template v-if="snap?.keyExpiry">{{ formatTime(snap.keyExpiry) }}</template>
          <template v-else-if="snap?.state === 'Running'"><NText type="success">{{ t('tsp.noExpiry') }}</NText></template>
          <template v-else>-</template>
        </NDescriptionsItem>
      </NDescriptions>
      <NSpace style="margin-top: 12px">
        <NButton v-if="canStart" type="primary" :loading="busy" @click="act(api.tailscaleStart, t('common.started'))">{{ t('common.start') }}</NButton>
        <NButton v-if="canStop && isLocal" :loading="busy" @click="act(api.tailscaleStop, t('common.stopped'))">{{ t('common.stop') }}</NButton>
        <NPopconfirm v-if="snap?.hasState && isLocal && !isSystem" @positive-click="act(api.tailscaleLogout, t('tsp.loggedOut'))">
          <template #trigger>
            <NButton type="error" ghost :loading="busy">{{ t('tsp.logoutReset') }}</NButton>
          </template>
          {{ t('tsp.logoutConfirm') }}
        </NPopconfirm>
        <NPopconfirm v-if="showBackendSwitch" @positive-click="switchBackend(isSystem ? 'embedded' : 'system')">
          <template #trigger>
            <NButton :loading="busy">{{ isSystem ? t('tsp.useEmbedded') : t('tsp.useSystem') }}</NButton>
          </template>
          {{ isSystem ? t('tsp.switchEmbeddedConfirm') : t('tsp.switchSystemConfirm') }}
        </NPopconfirm>
        <NText v-if="isSystem" depth="3">{{ t('tsp.systemManaged') }}</NText>
        <NText v-else-if="!isLocal" depth="3">{{ t('tsp.remoteOnly') }}</NText>
      </NSpace>
    </NCard>

    <NCard v-if="isSystem && (sysStep || job)" :title="t('tsp.setup')">
      <template v-if="sysStep">
        <NAlert :type="sysStep === 'firewall' ? 'info' : 'warning'" :show-icon="false" style="margin-bottom: 12px">
          <template v-if="sysStep === 'install'">
            {{ t('tsp.notInstalledHint') }}
            <template v-if="sys?.needMB && sys?.freeMB"><br />{{ t('tsp.space', { need: sys.needMB, free: sys.freeMB }) }}</template>
          </template>
          <template v-else-if="sysStep === 'start'">{{ t('tsp.notRunningHint') }}</template>
          <template v-else-if="sysStep === 'service'">{{ t('tsp.noServiceHint', { pkg: sys?.servicePackage ?? '' }) }}</template>
          <template v-else>{{ t('tsp.firewallHint') }}</template>
        </NAlert>
        <NSpace align="center">
          <template v-if="sys?.canManage">
            <NButton v-if="sysStep === 'install'" type="primary" :loading="job?.running" @click="runSystem('install')">{{ t('tsp.install') }}</NButton>
            <NButton v-else-if="sysStep === 'start'" type="primary" :loading="job?.running" @click="runSystem('start')">{{ t('tsp.startService') }}</NButton>
            <NButton v-else-if="sysStep === 'service'" type="primary" :loading="job?.running" @click="runSystem('service')">{{ t('tsp.installServicePkg', { pkg: sys?.servicePackage ?? '' }) }}</NButton>
            <NButton v-else type="primary" :loading="job?.running" @click="runSystem('firewall')">{{ t('tsp.fixFirewall') }}</NButton>
          </template>
          <NText v-else depth="3">{{ t('tsp.manualOnly') }}</NText>
        </NSpace>
        <div v-if="sysStep === 'install' && sys?.installCommand" class="hint">
          {{ t('tsp.manual') }} <code>{{ sys.installCommand }}</code>
        </div>
        <div v-else-if="sysStep === 'service' && sys?.serviceInstallCommand" class="hint">
          {{ t('tsp.manual') }} <code>{{ sys.serviceInstallCommand }}</code>
        </div>
        <div v-else-if="sysStep === 'start'" class="hint">
          {{ t('tsp.manual') }} <code>/etc/init.d/tailscale enable && /etc/init.d/tailscale start</code>
        </div>
        <div v-if="sysStep === 'install'" class="hint">{{ t('tsp.offlineHint') }}</div>
        <div v-if="sysStep === 'install' && showBackendSwitch" class="hint">
          {{ t('tsp.fallbackEmbedded') }}
          <NButton text type="primary" size="small" @click="switchBackend('embedded')">{{ t('tsp.useEmbedded') }}</NButton>
        </div>
      </template>
      <template v-if="job?.action">
        <NText :type="job.running ? 'info' : job.ok ? 'success' : 'error'" style="display: block; margin-top: 12px">
          {{ job.running ? t('tsp.jobRunning') : job.ok ? t('tsp.jobOK') : t('tsp.jobFailed', { e: job.error || '' }) }}
        </NText>
        <pre v-if="job.log" class="joblog">{{ job.log }}</pre>
      </template>
    </NCard>

    <NCard v-if="canLogin" :title="t('tsp.login')">
      <template v-if="isSystem">
        <NSpace align="center" style="margin-bottom: 12px">
          <NButton :loading="busy" @click="loginBrowser">{{ t('tsp.loginBrowser') }}</NButton>
          <NText depth="3" style="font-size: 12px">{{ t('tsp.loginBrowserHint') }}</NText>
        </NSpace>
        <NSpace v-if="snap?.authURL" align="center" style="margin-bottom: 16px">
          <NQrCode :value="snap.authURL" :size="140" />
          <div>
            <div>{{ t('tsp.scanQR') }}</div>
            <a :href="snap.authURL" target="_blank" rel="noopener">{{ snap.authURL }}</a>
          </div>
        </NSpace>
      </template>
      <NAlert type="info" style="margin-bottom: 12px">
        {{ t('tsp.loginHint1') }}
        {{ t('tsp.loginHint2') }}
      </NAlert>
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('common.authKey')">
          <NInput v-model:value="form.authKey" type="password" show-password-on="click" placeholder="tskey-auth-..." />
        </NFormItem>
        <NFormItem v-if="!isSystem" :label="t('tsp.hostname')">
          <NInput v-model:value="form.hostname" :placeholder="t('tsp.hostnamePh')" />
        </NFormItem>
        <NFormItem :label="t('tsp.control')">
          <NInput v-model:value="form.controlURL" :placeholder="t('tsp.controlPh')" />
        </NFormItem>
        <NFormItem :label="t('common.tags')">
          <NInput v-model:value="form.tags" :placeholder="t('tsp.tagsPh')" />
        </NFormItem>
        <NFormItem v-if="snap?.hasState && !isSystem" label=" ">
          <NText depth="3" style="font-size: 12px">{{ t('tsp.hasState') }}</NText>
        </NFormItem>
        <NFormItem label=" ">
          <NButton type="primary" :loading="busy" @click="login">{{ t('tsp.login') }}</NButton>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard v-if="snap?.state === 'Running'" :title="t('tsp.peers')">
      <template #header-extra>
        <NText depth="3" style="font-size: 12px">{{ t('tsp.homeDERP', { r: snap?.homeRelay || '-' }) }}</NText>
      </template>
      <NDataTable :columns="peerColumns" :data="peers" size="small" :bordered="false" :max-height="360" :scroll-x="960" />
    </NCard>

    <NCard v-if="snap?.state === 'Running' && !isSystem" :title="t('tsp.diag')">
      <template #header-extra>
        <NButton size="small" :loading="netcheckBusy" @click="runNetcheck">{{ t('tsp.runNetcheck') }}</NButton>
      </template>
      <NText v-if="!netcheck" depth="3">{{ t('tsp.netcheckHint') }}</NText>
      <template v-else>
        <NDescriptions :column="3" label-placement="left" size="small" bordered>
          <NDescriptionsItem label="UDP">
            <NTag size="small" :type="netcheck.udp ? 'success' : 'error'">{{ netcheck.udp ? t('tsp.available') : t('tsp.unavailable') }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem :label="t('tsp.hardNAT')">
            <NTag size="small" :type="netcheck.hardNAT ? 'warning' : 'success'">{{ yesNo(netcheck.hardNAT) }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem label="IPv4 / IPv6">{{ yesNo(netcheck.ipv4) }} / {{ yesNo(netcheck.ipv6) }}</NDescriptionsItem>
          <NDescriptionsItem label="UPnP / PMP / PCP">{{ yesNo(netcheck.upnp) }} / {{ yesNo(netcheck.pmp) }} / {{ yesNo(netcheck.pcp) }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('tsp.publicV4')">{{ netcheck.globalV4 || '-' }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('tsp.preferredDERP')">{{ netcheck.preferredDERP || '-' }}</NDescriptionsItem>
        </NDescriptions>
        <NText v-if="netcheck.hardNAT" type="warning" style="display: block; margin-top: 8px; font-size: 12px">
          {{ t('tsp.hardNATHint') }}
        </NText>
        <NDataTable
          style="margin-top: 12px"
          :columns="derpColumns"
          :data="netcheck.regions"
          size="small"
          :bordered="false"
        />
        <NText depth="3" style="font-size: 12px">{{ t('tsp.took', { n: netcheck.tookMs }) }}</NText>
      </template>
    </NCard>
  </NSpace>
</template>

<style scoped>
.hint {
  margin-top: 8px;
  font-size: 12px;
  opacity: 0.8;
}
.hint code,
.joblog {
  font-family: Consolas, 'Cascadia Mono', Menlo, monospace;
}
.joblog {
  max-height: 260px;
  overflow: auto;
  margin: 8px 0 0;
  padding: 8px;
  border-radius: 4px;
  background: rgba(128, 128, 128, 0.12);
  font-size: 12px;
  white-space: pre-wrap;
}
</style>
