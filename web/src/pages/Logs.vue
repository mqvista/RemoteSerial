<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref } from 'vue'
import { NDataTable, NSelect, NSpace, NSwitch, NTag, NText } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api'
import type { LogEntry } from '../api'
import { formatTime, t } from '../i18n'

const entries = ref<LogEntry[]>([])
const level = ref('debug')
const autoRefresh = ref(true)
let timer: number | undefined

const levelOptions = computed(() => [
  { label: t('logs.all'), value: 'debug' },
  { label: t('logs.info'), value: 'info' },
  { label: t('logs.warn'), value: 'warn' },
  { label: 'ERROR', value: 'error' },
])

async function refresh() {
  try {
    const r = await api.logs(500, level.value)
    entries.value = r.entries.slice().reverse()
  } catch {
    // 概览页已有错误提示，这里静默
  }
}

function levelType(l: string) {
  if (l === 'ERROR') return 'error'
  if (l === 'WARN') return 'warning'
  if (l === 'DEBUG') return 'default'
  return 'info'
}

const columns = computed<DataTableColumns<LogEntry>>(() => [
  {
    title: t('logs.time'),
    key: 'time',
    width: 190,
    render: (row) => formatTime(row.time),
  },
  {
    title: t('logs.level'),
    key: 'level',
    width: 90,
    render: (row) => h(NTag, { size: 'small', type: levelType(row.level) }, { default: () => row.level }),
  },
  { title: t('logs.msg'), key: 'msg' },
  {
    title: t('logs.attrs'),
    key: 'attrs',
    render: (row) => h(NText, { depth: 3, style: 'font-family: monospace; font-size: 12px' }, { default: () => row.attrs ?? '' }),
  },
])

onMounted(() => {
  refresh()
  timer = window.setInterval(() => autoRefresh.value && refresh(), 3000)
})
onUnmounted(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <NSpace align="center" style="margin-bottom: 12px">
    <NSelect v-model:value="level" :options="levelOptions" style="width: 160px" @update:value="refresh" />
    <NSwitch v-model:value="autoRefresh" />
    <NText depth="3">{{ t('logs.autoRefresh') }}</NText>
  </NSpace>
  <NDataTable :columns="columns" :data="entries" :bordered="false" size="small" :max-height="'calc(100vh - 180px)'" />
</template>
