<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NDynamicTags, NForm, NFormItem, NInput, NInputNumber, NSelect, NSpace, NSwitch, NText, useMessage } from 'naive-ui'
import { api } from '../api'
import type { Config } from '../api'
import { useStatus } from '../status'

const { status } = useStatus()
const message = useMessage()

const cfg = ref<Config | null>(null)
const form = ref({
  logLevel: 'info',
  allowedLogins: [] as string[],
  listen: '',
  webPort: 80,
  udpPort: 41641,
  hostname: '',
  controlURL: '',
  updateURL: '',
  updateAutoCheck: true,
})
const busy = ref(false)
const pending = ref<string[]>([])

const nodeStopped = computed(() => {
  const s = status.value?.tailscale.state
  return s === 'NotConfigured' || s === 'Stopped' || s === 'Error' || s === 'NeedsLogin'
})
const isLocal = computed(() => status.value?.source === 'local')

const levelOptions = [
  { label: 'debug', value: 'debug' },
  { label: 'info', value: 'info' },
  { label: 'warn', value: 'warn' },
  { label: 'error', value: 'error' },
]

async function load() {
  try {
    cfg.value = await api.settings()
    form.value = {
      logLevel: cfg.value.logLevel,
      allowedLogins: [...cfg.value.access.allowedLogins],
      listen: cfg.value.listen,
      webPort: cfg.value.tailscale.webPort,
      udpPort: cfg.value.tailscale.udpPort,
      hostname: cfg.value.tailscale.hostname,
      controlURL: cfg.value.tailscale.controlURL,
      updateURL: cfg.value.update?.url ?? '',
      updateAutoCheck: cfg.value.update?.autoCheck ?? true,
    }
  } catch (e) {
    message.error(String(e))
  }
}

async function save() {
  busy.value = true
  try {
    const r = await api.saveSettings({
      logLevel: form.value.logLevel,
      allowedLogins: form.value.allowedLogins,
      listen: form.value.listen,
      webPort: form.value.webPort,
      udpPort: form.value.udpPort,
      hostname: nodeStopped.value ? form.value.hostname : undefined,
      controlURL: nodeStopped.value ? form.value.controlURL : undefined,
      updateURL: form.value.updateURL,
      updateAutoCheck: form.value.updateAutoCheck,
    })
    cfg.value = r.config
    pending.value = r.pending
    message.success(r.restartRequired ? '已保存，部分项需要重启程序后生效' : '已保存')
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
    <NAlert v-if="pending.length" type="warning">以下项已保存，重启程序后生效：{{ pending.join('、') }}</NAlert>

    <NCard title="访问控制">
      <NForm label-placement="left" label-width="140">
        <NFormItem label="允许的登录名">
          <NSpace vertical style="width: 100%">
            <NDynamicTags v-model:value="form.allowedLogins" />
            <NText depth="3" style="font-size: 12px">
              只有这些 Tailscale 登录名能从 tailnet 访问管理界面，例如 you@gmail.com。留空则 tailnet 内所有用户都能访问。远端本机窗口不受限制。
            </NText>
          </NSpace>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard title="运行">
      <NForm label-placement="left" label-width="140">
        <NFormItem label="日志级别">
          <NSelect v-model:value="form.logLevel" :options="levelOptions" style="width: 160px" />
        </NFormItem>
        <NFormItem label="本机监听地址">
          <NInput v-model:value="form.listen" placeholder="127.0.0.1:7890" style="width: 240px" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">重启生效</NText>
        </NFormItem>
        <NFormItem label="tailnet 管理端口">
          <NInputNumber v-model:value="form.webPort" :min="1" :max="65535" style="width: 160px" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">重启生效</NText>
        </NFormItem>
        <NFormItem label="WireGuard UDP 端口">
          <NInputNumber v-model:value="form.udpPort" :min="0" :max="65535" style="width: 160px" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">重启生效；防火墙规则按此端口放行，0 为随机</NText>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard title="Tailscale 节点">
      <NAlert v-if="!nodeStopped" type="info" style="margin-bottom: 12px">节点运行中，节点名与控制服务器需先在 Tailscale 页停止节点后修改。</NAlert>
      <NForm label-placement="left" label-width="140">
        <NFormItem label="节点名">
          <NInput v-model:value="form.hostname" :disabled="!nodeStopped" style="width: 240px" />
        </NFormItem>
        <NFormItem label="控制服务器">
          <NInput v-model:value="form.controlURL" :disabled="!nodeStopped" placeholder="留空用官方；Headscale 填 https://..." style="width: 360px" />
        </NFormItem>
      </NForm>
    </NCard>

    <NCard title="更新">
      <NForm label-placement="left" label-width="140">
        <NFormItem label="清单地址">
          <NInput v-model:value="form.updateURL" placeholder="https://.../manifest.json" style="width: 100%; max-width: 560px" />
        </NFormItem>
        <NFormItem label="自动检查">
          <NSwitch v-model:value="form.updateAutoCheck" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">启动 2 分钟后和每天检查一次，只提示不自动安装。私有仓库或内网可把清单放到自己的服务器上。</NText>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard title="信息">
      <NText depth="3">数据目录：{{ status?.dataDir }}</NText><br />
      <NText depth="3">版本：{{ status?.version }} ({{ status?.commit }})</NText><br />
      <NText v-if="!isLocal" depth="3">当前通过 tailnet 访问。</NText>
    </NCard>

    <NSpace>
      <NButton type="primary" :loading="busy" @click="save">保存</NButton>
      <NButton @click="load">重新读取</NButton>
    </NSpace>
  </NSpace>
</template>
