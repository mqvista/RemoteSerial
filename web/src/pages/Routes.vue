<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NDynamicInput, NList, NListItem, NSpace, NTag, NText, NThing, useMessage } from 'naive-ui'
import { api } from '../api'
import type { InterfaceInfo, RouteStatus } from '../api'
import { useStatus } from '../status'

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
  if (!nodeRunning.value) return { type: 'default' as const, label: '节点未运行' }
  return saved.value.approved.includes(cidr) ? { type: 'success' as const, label: '已批准' } : { type: 'warning' as const, label: '等待批准' }
}

function add(cidr: string) {
  if (!routes.value.includes(cidr)) routes.value.push(cidr)
}

async function save() {
  busy.value = true
  try {
    saved.value = await api.setRoutes(routes.value.map((r) => r.trim()).filter(Boolean))
    routes.value = [...saved.value.advertised]
    message.success('已保存并宣告')
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
    <NAlert type="info">
      子网路由把远端局域网宣告到 tailnet，本地设备就能直接访问远端网段里的其他设备。宣告后需要在 Tailscale 后台批准，或在 ACL 里为
      tag 配置 autoApprovers。用户态转发只支持 TCP 和 UDP，ping 远端设备不通属正常；源地址会做 SNAT。不要宣告与本地网段重叠的网段。
    </NAlert>

    <NCard title="宣告的网段">
      <template #header-extra>
        <NButton type="primary" size="small" :loading="busy" :disabled="!dirty" @click="save">保存并宣告</NButton>
      </template>
      <NDynamicInput v-model:value="routes" placeholder="如 192.168.10.0/24" :min="0">
        <template #default="{ index }">
          <NSpace align="center" style="width: 100%">
            <input v-model="routes[index]" class="cidr-input" placeholder="192.168.10.0/24" />
            <NTag v-if="routes[index]" size="small" :type="approvedTag(routes[index]).type">{{ approvedTag(routes[index]).label }}</NTag>
          </NSpace>
        </template>
      </NDynamicInput>
      <NText v-if="!routes.length" depth="3">尚未宣告任何网段。</NText>
    </NCard>

    <NCard title="本机所在网段">
      <template #header-extra>
        <NButton size="small" quaternary @click="showHidden = !showHidden">{{ showHidden ? '隐藏虚拟网卡' : '显示全部' }}</NButton>
      </template>
      <NList hoverable clickable>
        <NListItem v-for="i in visibleIfaces" :key="i.name + i.cidr" @click="add(i.cidr)">
          <NThing :title="i.cidr" :description="`${i.name} · 本机 ${i.ip}`">
            <template #header-extra>
              <NTag v-if="routes.includes(i.cidr)" size="small" type="success">已加入</NTag>
              <NButton v-else size="tiny">加入</NButton>
            </template>
          </NThing>
        </NListItem>
      </NList>
      <NText v-if="!visibleIfaces.length" depth="3">没有可用的网段。</NText>
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
