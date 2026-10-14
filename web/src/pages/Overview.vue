<script setup lang="ts">
import { computed, h } from 'vue'
import { RouterLink } from 'vue-router'
import { NAlert, NCard, NDataTable, NDescriptions, NDescriptionsItem, NGrid, NGi, NStatistic, NTag, NText } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { formatBytes, formatUptime } from '../api'
import type { BridgeStatus } from '../api'
import { useStatus } from '../status'

const { status, connected, error } = useStatus()

const ts = computed(() => status.value?.tailscale)

function tsTagType(state?: string) {
  if (state === 'Running') return 'success'
  if (state === 'Starting') return 'info'
  if (state === 'Error') return 'error'
  return 'warning'
}
const tsLabel = computed(() => {
  const m: Record<string, string> = {
    NotConfigured: '未登录',
    Starting: '连接中',
    NeedsLogin: '需要登录',
    NeedsMachineAuth: '等待批准',
    Running: '在线',
    Stopped: '已停止',
    Error: '错误',
  }
  return m[ts.value?.state ?? ''] ?? ts.value?.state ?? '-'
})

const connectedCount = computed(() => (status.value?.bridges ?? []).filter((b) => b.state === 'Connected').length)

const bridgeColumns: DataTableColumns<BridgeStatus> = [
  { title: '名称', key: 'name', minWidth: 110, ellipsis: { tooltip: true }, render: (b) => b.name || b.id },
  { title: '串口', key: 'port', width: 110, ellipsis: { tooltip: true }, render: (b) => b.resolvedPort || b.port },
  { title: 'TCP', key: 'tcpPort', width: 70 },
  {
    title: '状态',
    key: 'state',
    width: 110,
    render: (b) => {
      const map: Record<string, [string, 'default' | 'info' | 'success' | 'warning' | 'error']> = {
        Disabled: ['已停用', 'default'],
        WaitingNetwork: ['等待节点', 'warning'],
        WaitingDevice: ['等待设备', 'warning'],
        Listening: ['监听中', 'info'],
        Connected: ['已连接', 'success'],
        Error: ['错误', 'error'],
      }
      const [label, type] = map[b.state] ?? [b.state, 'default']
      return h(NTag, { size: 'small', type }, { default: () => label })
    },
  },
  { title: '客户端', key: 'client', minWidth: 150, ellipsis: { tooltip: true }, render: (b) => b.client || '-' },
  { title: '流量', key: 'bytes', width: 150, render: (b) => `↓ ${formatBytes(b.fromDevice)} ↑ ${formatBytes(b.toDevice)}` },
  { title: '错误', key: 'lastError', minWidth: 120, ellipsis: { tooltip: true }, render: (b) => h(NText, { type: 'error', style: 'font-size:12px' }, { default: () => b.lastError || '' }) },
]
</script>

<template>
  <NAlert v-if="error" type="error" title="无法连接 core" style="margin-bottom: 16px">{{ error }}</NAlert>
  <NAlert v-else-if="!connected" type="warning" style="margin-bottom: 16px">实时推送未连接，正在轮询。</NAlert>
  <NAlert v-if="status?.accessWarning && status?.source === 'tailnet'" type="warning" style="margin-bottom: 16px">
    访问白名单为空，tailnet 内任何用户都能打开此页面。请在"设置"里填写允许的登录名。
  </NAlert>
  <NAlert v-if="status?.update?.available" type="info" style="margin-bottom: 16px">
    有新版本 {{ status.update.latest }}<template v-if="status.update.downloaded === status.update.latest">，已下载</template>，到
    <RouterLink to="/deploy">部署</RouterLink> 页更新。
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
        <NStatistic label="节点 IP" :value="ts?.ip || '-'" />
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="桥接" :value="`${connectedCount} / ${status.bridges.length}`">
          <template #suffix><NText depth="3" style="font-size: 12px">已连接 / 总数</NText></template>
        </NStatistic>
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="运行时长" :value="formatUptime(status.uptimeSec)" />
      </NCard>
    </NGi>
    <NGi span="4">
      <NCard title="桥接">
        <NDataTable :columns="bridgeColumns" :data="status.bridges" size="small" :bordered="false" :scroll-x="720" />
      </NCard>
    </NGi>
    <NGi span="4">
      <NCard title="运行信息">
        <NDescriptions :column="2" label-placement="left" size="small">
          <NDescriptionsItem label="MagicDNS">{{ ts?.dnsName || '-' }}</NDescriptionsItem>
          <NDescriptionsItem label="tailnet 管理地址">
            <template v-if="ts?.dnsName">http://{{ ts.dnsName }}{{ status.webPort === 80 ? '' : ':' + status.webPort }}/</template>
            <template v-else>-</template>
          </NDescriptionsItem>
          <NDescriptionsItem label="版本">{{ status.version }} ({{ status.commit }})</NDescriptionsItem>
          <NDescriptionsItem label="运行模式">{{ status.mode }}</NDescriptionsItem>
          <NDescriptionsItem label="数据目录">{{ status.dataDir }}</NDescriptionsItem>
          <NDescriptionsItem label="请求来源">
            {{ status.source }}
            <template v-if="status.identity"> · {{ status.identity.tags?.length ? status.identity.node : status.identity.loginName }}</template>
          </NDescriptionsItem>
        </NDescriptions>
      </NCard>
    </NGi>
  </NGrid>
</template>
