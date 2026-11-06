<script setup lang="ts">
import { computed, h } from 'vue'
import { RouterLink } from 'vue-router'
import { NAlert, NCard, NDataTable, NDescriptions, NDescriptionsItem, NGrid, NGi, NStatistic, NTag, NText } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { formatBytes, formatUptime } from '../api'
import type { BridgeStatus } from '../api'
import { useStatus } from '../status'
import { t, tOr } from '../i18n'

const { status, connected, error } = useStatus()

const ts = computed(() => status.value?.tailscale)

function tsTagType(state?: string) {
  if (state === 'Running') return 'success'
  if (state === 'Starting') return 'info'
  if (state === 'Error') return 'error'
  return 'warning'
}
const tsLabel = computed(() => {
  const s = ts.value?.state
  return s ? tOr(`ts.${s}`, s) : '-'
})

const connectedCount = computed(() => (status.value?.bridges ?? []).filter((b) => b.state === 'Connected').length)

type TagType = 'default' | 'info' | 'success' | 'warning' | 'error'
const bridgeTypes: Record<string, TagType> = {
  Disabled: 'default',
  WaitingNetwork: 'warning',
  WaitingDevice: 'warning',
  Listening: 'info',
  Connected: 'success',
  Error: 'error',
}

const bridgeColumns = computed<DataTableColumns<BridgeStatus>>(() => [
  { title: t('col.name'), key: 'name', minWidth: 110, ellipsis: { tooltip: true }, render: (b) => b.name || b.id },
  { title: t('col.port'), key: 'port', minWidth: 200, ellipsis: { tooltip: true }, render: (b) => b.resolvedPort || b.port },
  { title: 'TCP', key: 'tcpPort', width: 70 },
  {
    title: t('col.state'),
    key: 'state',
    width: 150,
    render: (b) => h(NTag, { size: 'small', type: bridgeTypes[b.state] ?? 'default' }, { default: () => tOr(`bridgeState.${b.state}`, b.state) }),
  },
  { title: t('col.client'), key: 'client', minWidth: 150, ellipsis: { tooltip: true }, render: (b) => b.client || '-' },
  { title: t('col.traffic'), key: 'bytes', width: 160, render: (b) => `↓ ${formatBytes(b.fromDevice)} ↑ ${formatBytes(b.toDevice)}` },
  { title: t('col.error'), key: 'lastError', minWidth: 120, ellipsis: { tooltip: true }, render: (b) => h(NText, { type: 'error', style: 'font-size:12px' }, { default: () => b.lastError || '' }) },
])
</script>

<template>
  <NAlert v-if="error" type="error" :title="t('overview.coreUnreachable')" style="margin-bottom: 16px">{{ error }}</NAlert>
  <NAlert v-else-if="!connected" type="warning" style="margin-bottom: 16px">{{ t('overview.polling') }}</NAlert>
  <NAlert v-if="status?.accessWarning && status?.source === 'tailnet'" type="warning" style="margin-bottom: 16px">
    {{ t('overview.accessWarning') }}
  </NAlert>
  <NAlert v-if="status?.update?.available" type="info" style="margin-bottom: 16px">
    {{ t('overview.updateAvailable', { v: status.update.latest ?? '' }) }}<template v-if="status.update.downloaded === status.update.latest">{{ t('overview.updateDownloaded') }}</template>{{ t('overview.updateGoto') }}
    <RouterLink to="/deploy">{{ t('overview.updateGotoPage') }}</RouterLink> {{ t('overview.updateGotoSuffix') }}
  </NAlert>

  <NGrid v-if="status" :cols="4" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="Tailscale">
          <NTag :type="tsTagType(ts?.state)">{{ tsLabel }}</NTag>
        </NStatistic>
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic :label="t('overview.nodeIP')" :value="ts?.ip || '-'" />
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic :label="t('overview.bridges')" :value="`${connectedCount} / ${status.bridges.length}`">
          <template #suffix><NText depth="3" style="font-size: 12px">{{ t('overview.connectedTotal') }}</NText></template>
        </NStatistic>
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic :label="t('overview.uptime')" :value="formatUptime(status.uptimeSec)" />
      </NCard>
    </NGi>
    <NGi span="4">
      <NCard :title="t('overview.bridges')">
        <NDataTable :columns="bridgeColumns" :data="status.bridges" size="small" :bordered="false" :scroll-x="860" />
      </NCard>
    </NGi>
    <NGi span="4">
      <NCard :title="t('overview.runtime')">
        <NDescriptions :column="2" label-placement="left" size="small">
          <NDescriptionsItem label="MagicDNS">{{ ts?.dnsName || '-' }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('overview.adminURL')">
            <template v-if="ts?.dnsName">http://{{ ts.dnsName }}{{ status.webPort === 80 ? '' : ':' + status.webPort }}/</template>
            <template v-else>-</template>
          </NDescriptionsItem>
          <NDescriptionsItem :label="t('overview.version')">{{ status.version }} ({{ status.commit }})</NDescriptionsItem>
          <NDescriptionsItem :label="t('overview.mode')">{{ tOr(`mode.${status.mode}`, status.mode) }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('overview.dataDir')">{{ status.dataDir }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('overview.source')">
            {{ status.source }}
            <template v-if="status.identity"> · {{ status.identity.tags?.length ? status.identity.node : status.identity.loginName }}</template>
          </NDescriptionsItem>
        </NDescriptions>
      </NCard>
    </NGi>
  </NGrid>
</template>
