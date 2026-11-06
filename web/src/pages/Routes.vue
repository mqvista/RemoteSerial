<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NDynamicInput, NList, NListItem, NSpace, NTag, NText, NThing, useMessage } from 'naive-ui'
import { api } from '../api'
import type { InterfaceInfo, RouteStatus } from '../api'
import { useStatus } from '../status'
import { t } from '../i18n'

const { status } = useStatus()
const message = useMessage()

const routes = ref<string[]>([])
const saved = ref<RouteStatus>({ advertised: [], approved: [] })
const ifaces = ref<InterfaceInfo[]>([])
const showHidden = ref(false)
const busy = ref(false)

const nodeRunning = computed(() => status.value?.tailscale.state === 'Running')
const dirty = computed(() => JSON.stringify(routes.value.filter(Boolean)) !== JSON.stringify(saved.value.advertised))
const visibleIfaces = computed(() => ifaces.value.filter((i) => showHidden.value || !i.hidden))

async function load() {
  try {
    saved.value = await api.routes()
    routes.value = [...saved.value.advertised]
  } catch (e) {
    message.error(String(e))
  }
  try {
    ifaces.value = (await api.interfaces()).interfaces
  } catch {
    // 忽略
  }
}

function approvedTag(cidr: string) {
  if (!nodeRunning.value) return { type: 'default' as const, label: t('routes.nodeNotRunning') }
  return saved.value.approved.includes(cidr) ? { type: 'success' as const, label: t('routes.approved') } : { type: 'warning' as const, label: t('routes.awaiting') }
}

function add(cidr: string) {
  if (!routes.value.includes(cidr)) routes.value.push(cidr)
}

async function save() {
  busy.value = true
  try {
    saved.value = await api.setRoutes(routes.value.map((r) => r.trim()).filter(Boolean))
    routes.value = [...saved.value.advertised]
    message.success(t('routes.savedAdvertised'))
  } catch (e) {
    message.error(String(e))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <NSpace vertical :size="16">
    <NAlert type="info">{{ t('routes.intro') }}</NAlert>

    <NCard :title="t('routes.advertised')">
      <template #header-extra>
        <NButton type="primary" size="small" :loading="busy" :disabled="!dirty" @click="save">{{ t('routes.saveAdvertise') }}</NButton>
      </template>
      <NDynamicInput v-model:value="routes" :placeholder="t('routes.placeholder')" :min="0">
        <template #default="{ index }">
          <NSpace align="center" style="width: 100%">
            <input v-model="routes[index]" class="cidr-input" placeholder="192.168.10.0/24" />
            <NTag v-if="routes[index]" size="small" :type="approvedTag(routes[index]).type">{{ approvedTag(routes[index]).label }}</NTag>
          </NSpace>
        </template>
      </NDynamicInput>
      <NText v-if="!routes.length" depth="3">{{ t('routes.none') }}</NText>
    </NCard>

    <NCard :title="t('routes.local')">
      <template #header-extra>
        <NButton size="small" quaternary @click="showHidden = !showHidden">{{ showHidden ? t('routes.hideVirtual') : t('routes.showAll') }}</NButton>
      </template>
      <NList hoverable clickable>
        <NListItem v-for="i in visibleIfaces" :key="i.name + i.cidr" @click="add(i.cidr)">
          <NThing :title="i.cidr" :description="t('routes.localIP', { name: i.name, ip: i.ip })">
            <template #header-extra>
              <NTag v-if="routes.includes(i.cidr)" size="small" type="success">{{ t('routes.added') }}</NTag>
              <NButton v-else size="tiny">{{ t('routes.add') }}</NButton>
            </template>
          </NThing>
        </NListItem>
      </NList>
      <NText v-if="!visibleIfaces.length" depth="3">{{ t('routes.noIfaces') }}</NText>
    </NCard>
  </NSpace>
</template>

<style scoped>
.cidr-input {
  width: 260px;
  padding: 6px 10px;
  border: 1px solid rgba(128, 128, 128, 0.4);
  border-radius: 3px;
  font-family: monospace;
  background: transparent;
  color: inherit;
}
</style>
