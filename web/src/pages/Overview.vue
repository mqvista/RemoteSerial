<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { NAlert, NCard, NDescriptions, NDescriptionsItem, NGrid, NGi, NStatistic, NTag } from 'naive-ui'
import { api, formatUptime } from '../api'
import type { Status } from '../api'

const status = ref<Status | null>(null)
const error = ref('')
let timer: number | undefined

async function refresh() {
  try {
    status.value = await api.status()
    error.value = ''
  } catch (e) {
    error.value = String(e)
  }
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 5000)
})
onUnmounted(() => {
  if (timer) window.clearInterval(timer)
})

function tsTagType(state: string) {
  if (state === 'Running') return 'success'
  if (state === 'NotConfigured' || state === 'NeedsLogin') return 'warning'
  return 'default'
}
</script>

<template>
  <NAlert v-if="error" type="error" title="无法连接 core" style="margin-bottom: 16px">{{ error }}</NAlert>

  <NGrid v-if="status" :cols="4" :x-gap="16" :y-gap="16" responsive="screen" item-responsive>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="版本" :value="status.version" />
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="运行模式" :value="status.mode" />
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="运行时长" :value="formatUptime(status.uptimeSec)" />
      </NCard>
    </NGi>
    <NGi span="4 m:2 l:1">
      <NCard>
        <NStatistic label="桥接数" :value="status.bridges.length" />
      </NCard>
    </NGi>
    <NGi span="4">
      <NCard title="Tailscale">
        <NDescriptions :column="3" label-placement="left">
          <NDescriptionsItem label="状态">
            <NTag :type="tsTagType(status.tailscale.state)" size="small">{{ status.tailscale.state }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem label="节点名">{{ status.tailscale.hostname }}</NDescriptionsItem>
          <NDescriptionsItem label="IP">{{ status.tailscale.ip || '-' }}</NDescriptionsItem>
        </NDescriptions>
      </NCard>
    </NGi>
    <NGi span="4">
      <NCard title="运行信息">
        <NDescriptions :column="1" label-placement="left">
          <NDescriptionsItem label="数据目录">{{ status.dataDir }}</NDescriptionsItem>
          <NDescriptionsItem label="本机监听">{{ status.listen }}</NDescriptionsItem>
          <NDescriptionsItem label="请求来源">{{ status.source }}</NDescriptionsItem>
          <NDescriptionsItem label="提交">{{ status.commit }}</NDescriptionsItem>
        </NDescriptions>
      </NCard>
    </NGi>
  </NGrid>
</template>
