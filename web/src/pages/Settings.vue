<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NDynamicTags, NForm, NFormItem, NInput, NInputNumber, NSelect, NSpace, NSwitch, NText, useMessage } from 'naive-ui'
import { api } from '../api'
import type { Config } from '../api'
import { useStatus } from '../status'
import { t } from '../i18n'
import type { MessageKey } from '../i18n'

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
  tags: [] as string[],
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
const pendingNames: Record<string, MessageKey> = { listen: 'settings.listen', webPort: 'settings.webPort', udpPort: 'settings.udpPort' }
const pendingLabels = computed(() => pending.value.map((k) => (pendingNames[k] ? t(pendingNames[k]) : k)).join(', '))

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
      tags: [...(cfg.value.tailscale.tags ?? [])],
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
      tags: nodeStopped.value ? form.value.tags : undefined,
      updateURL: form.value.updateURL,
      updateAutoCheck: form.value.updateAutoCheck,
    })
    cfg.value = r.config
    pending.value = r.pending
    message.success(r.restartRequired ? t('settings.savedRestart') : t('common.saved'))
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
    <NAlert v-if="pending.length" type="warning">{{ t('settings.pending', { items: pendingLabels }) }}</NAlert>

    <NCard :title="t('settings.access')">
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('settings.allowedLogins')">
          <NSpace vertical style="width: 100%">
            <NDynamicTags v-model:value="form.allowedLogins" />
            <NText depth="3" style="font-size: 12px">
              {{ t('settings.allowedHint') }}
            </NText>
          </NSpace>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard :title="t('settings.runtime')">
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('settings.logLevel')">
          <NSelect v-model:value="form.logLevel" :options="levelOptions" style="width: 160px" />
        </NFormItem>
        <NFormItem :label="t('settings.listen')">
          <NInput v-model:value="form.listen" placeholder="127.0.0.1:7890" style="width: 240px" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">{{ t('common.restartToApply') }}</NText>
        </NFormItem>
        <NFormItem :label="t('settings.webPort')">
          <NInputNumber v-model:value="form.webPort" :min="1" :max="65535" style="width: 160px" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">{{ t('common.restartToApply') }}</NText>
        </NFormItem>
        <NFormItem :label="t('settings.udpPort')">
          <NInputNumber v-model:value="form.udpPort" :min="0" :max="65535" style="width: 160px" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">{{ t('settings.udpHint') }}</NText>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard :title="t('settings.node')">
      <NAlert v-if="!nodeStopped" type="info" style="margin-bottom: 12px">{{ t('settings.nodeRunning') }}</NAlert>
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('settings.hostname')">
          <NInput v-model:value="form.hostname" :disabled="!nodeStopped" style="width: 240px" />
        </NFormItem>
        <NFormItem :label="t('settings.control')">
          <NInput v-model:value="form.controlURL" :disabled="!nodeStopped" :placeholder="t('settings.controlPh')" style="width: 360px" />
        </NFormItem>
        <NFormItem label="Tag">
          <NSpace vertical style="width: 100%">
            <NDynamicTags v-model:value="form.tags" :disabled="!nodeStopped" />
            <NText depth="3" style="font-size: 12px">{{ t('settings.tagsHint') }}</NText>
          </NSpace>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard :title="t('settings.update')">
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('settings.manifest')">
          <NInput v-model:value="form.updateURL" placeholder="https://.../manifest.json" style="width: 100%; max-width: 560px" />
        </NFormItem>
        <NFormItem :label="t('settings.autoCheck')">
          <NSwitch v-model:value="form.updateAutoCheck" />
          <NText depth="3" style="margin-left: 12px; font-size: 12px">{{ t('settings.autoCheckHint') }}</NText>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard :title="t('settings.info')">
      <NText depth="3">{{ t('settings.dataDir', { d: status?.dataDir ?? '' }) }}</NText><br />
      <NText depth="3">{{ t('settings.version', { v: `${status?.version ?? ''} (${status?.commit ?? ''})` }) }}</NText><br />
      <NText v-if="!isLocal" depth="3">{{ t('settings.viaTailnet') }}</NText>
    </NCard>

    <NSpace>
      <NButton type="primary" :loading="busy" @click="save">{{ t('common.save') }}</NButton>
      <NButton @click="load">{{ t('settings.reread') }}</NButton>
    </NSpace>
  </NSpace>
</template>
