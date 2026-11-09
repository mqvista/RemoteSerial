<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NDynamicTags, NForm, NFormItem, NInput, NInputNumber, NSelect, NSpace, NSwitch, NText, useMessage } from 'naive-ui'
import { api } from '../api'
import type { Config, ConsoleInfo } from '../api'
import { useStatus } from '../status'
import { t } from '../i18n'
import type { MessageKey } from '../i18n'

const { status } = useStatus()
const message = useMessage()

const cfg = ref<Config | null>(null)
const form = ref({
  logLevel: 'info',
  allowedLogins: [] as string[],
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
const source = computed(() => status.value?.source)
const isOpenWrt = computed(() => status.value?.platform === 'openwrt')
const pendingNames: Record<string, MessageKey> = { listen: 'settings.listen', webPort: 'settings.webPort', udpPort: 'settings.udpPort' }

// ---- 管理界面存取：端口、局域网访问、局域网密码（立即生效）----
const con = ref<ConsoleInfo | null>(null)
const conForm = ref({ port: 7890, lan: false })
const conBusy = ref(false)
const newPassword = ref('')
const pwBusy = ref(false)
const conDirty = computed(() => !!con.value && (conForm.value.port !== con.value.port || conForm.value.lan !== con.value.lan))

async function loadConsole() {
  try {
    con.value = await api.consoleInfo()
    conForm.value = { port: con.value.port, lan: con.value.lan }
  } catch (e) {
    message.error(String(e))
  }
}

function pagePort(): number {
  if (location.port) return Number(location.port)
  return location.protocol === 'https:' ? 443 : 80
}

async function applyConsole() {
  if (!con.value) return
  const oldPort = con.value.port
  const oldWeb = con.value.webPort
  conBusy.value = true
  try {
    const r = await api.setConsole({ port: conForm.value.port, lan: conForm.value.lan })
    con.value = r
    conForm.value = { port: r.port, lan: r.lan }
    if (r.warning) message.warning(r.warning)
    // 正在用的端口换掉了：跳到新端口（会话 Cookie 不分端口，仍然有效）
    const here = pagePort()
    if (r.live && ((r.port !== oldPort && here === oldPort) || (r.webPort !== oldWeb && here === oldWeb))) {
      const next = here === oldPort ? r.port : r.webPort
      message.success(t('console.moved', { p: next }))
      window.setTimeout(() => {
        location.href = `${location.protocol}//${location.hostname}:${next}${location.pathname}`
      }, 1200)
    } else {
      message.success(r.live ? t('common.saved') : t('settings.savedRestart'))
    }
  } catch (e) {
    message.error(String(e))
  } finally {
    conBusy.value = false
  }
}

async function savePassword(clear: boolean) {
  pwBusy.value = true
  try {
    const r = await api.setPassword(clear ? '' : newPassword.value)
    if (con.value) con.value.passwordSet = r.passwordSet
    newPassword.value = ''
    message.success(clear ? t('console.passwordCleared') : t('console.passwordSaved'))
  } catch (e) {
    message.error(String(e))
  } finally {
    pwBusy.value = false
  }
}
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
      allowedLogins: [...(cfg.value.access.allowedLogins ?? [])],
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
      // OpenWrt 上 tailnet 侧与本机共用端口，由“管理介面存取”一起修改
      webPort: isOpenWrt.value ? undefined : form.value.webPort,
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

onMounted(() => {
  load()
  loadConsole()
})
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

    <NCard v-if="con" :title="t('console.title')">
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('console.port')">
          <NSpace vertical :size="4">
            <NInputNumber v-model:value="conForm.port" :min="1" :max="65535" style="width: 160px" />
            <NText depth="3" style="font-size: 12px">{{ con.live ? t('console.portHint') : t('common.restartToApply') }}</NText>
          </NSpace>
        </NFormItem>
        <NFormItem :label="t('console.lan')">
          <NSpace vertical :size="4">
            <NSwitch v-model:value="conForm.lan" :disabled="source === 'lan'" />
            <NText depth="3" style="font-size: 12px">{{ t('console.lanHint') }}</NText>
          </NSpace>
        </NFormItem>
        <NFormItem label=" ">
          <NButton type="primary" :disabled="!conDirty" :loading="conBusy" @click="applyConsole">{{ t('console.apply') }}</NButton>
        </NFormItem>
        <NFormItem :label="t('console.password')">
          <NSpace vertical :size="4">
            <NSpace align="center">
              <NText :type="con.passwordSet ? 'success' : 'default'">{{ con.passwordSet ? t('console.passwordSet') : t('console.passwordUnset') }}</NText>
              <NInput
                v-model:value="newPassword"
                type="password"
                show-password-on="click"
                :placeholder="t('console.passwordPh')"
                :input-props="{ autocomplete: 'new-password' }"
                style="width: 220px"
              />
              <NButton :disabled="newPassword.length < 8" :loading="pwBusy" @click="savePassword(false)">{{ t('console.setPassword') }}</NButton>
              <NButton v-if="con.passwordSet" quaternary type="error" :loading="pwBusy" @click="savePassword(true)">{{ t('console.clearPassword') }}</NButton>
            </NSpace>
            <NText depth="3" style="font-size: 12px">{{ t('console.passwordHint') }}</NText>
          </NSpace>
        </NFormItem>
      </NForm>
    </NCard>

    <NCard :title="t('settings.runtime')">
      <NForm label-placement="left" label-width="auto">
        <NFormItem :label="t('settings.logLevel')">
          <NSelect v-model:value="form.logLevel" :options="levelOptions" style="width: 160px" />
        </NFormItem>
        <NFormItem v-if="!isOpenWrt" :label="t('settings.webPort')">
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
        <NFormItem :label="t('common.tags')">
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
      <NText v-if="source === 'tailnet'" depth="3">{{ t('settings.viaTailnet') }}</NText>
      <NText v-else-if="source === 'lan'" depth="3">{{ t('settings.viaLAN') }}</NText>
    </NCard>

    <NSpace>
      <NButton type="primary" :loading="busy" @click="save">{{ t('common.save') }}</NButton>
      <NButton @click="load">{{ t('settings.reread') }}</NButton>
    </NSpace>
  </NSpace>
</template>
