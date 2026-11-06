<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NCard, NDescriptions, NDescriptionsItem, NPopconfirm, NProgress, NSpace, NTag, NText, useMessage } from 'naive-ui'
import { api, formatBytes } from '../api'
import type { DeployAction, DeployStatus, UpdateStatus } from '../api'
import { useStatus } from '../status'
import { formatTime, t, tOr } from '../i18n'

const { status, connected } = useStatus()
const message = useMessage()

const st = ref<DeployStatus | null>(null)
const busy = ref<string | null>(null)
const lastMessage = ref('')
// core 正在服务与单进程之间切换：期间不刷新，也不报连接错误
const switching = ref(false)
const switchingActions = new Set(['service-install', 'service-start', 'service-stop', 'service-remove'])

// 更新
const upd = ref<UpdateStatus | null>(null)
const reloadOnReconnect = ref(false)
let dlTimer: number | undefined
const pct = computed(() => (upd.value && upd.value.total > 0 ? Math.min(100, Math.round((upd.value.received / upd.value.total) * 100)) : 0))
const canDownload = computed(() => !!upd.value?.available && !upd.value.downloading && upd.value.downloaded?.version !== upd.value.latest?.version)

async function loadUpdate() {
  try {
    upd.value = await api.updateStatus()
  } catch {
    // core 不支持或正在切换
  }
}
async function updateCheck() {
  try {
    upd.value = await api.updateCheck()
    if (upd.value.available) message.info(t('deploy.newVersion', { v: upd.value.latest?.version ?? '' }))
    else message.success(t('deploy.isLatest'))
  } catch (e) {
    message.error(String(e))
    loadUpdate()
  }
}
async function updateDownload() {
  try {
    upd.value = await api.updateDownload()
    if (dlTimer) window.clearInterval(dlTimer)
    dlTimer = window.setInterval(async () => {
      await loadUpdate()
      if (!upd.value?.downloading) {
        window.clearInterval(dlTimer)
        dlTimer = undefined
        if (upd.value?.error) message.error(upd.value.error)
        else if (upd.value?.downloaded) message.success(t('deploy.downloadedMsg', { v: upd.value.downloaded.version }))
      }
    }, 1000)
  } catch (e) {
    message.error(String(e))
  }
}
async function updateCancel() {
  try {
    upd.value = await api.updateCancel()
  } catch (e) {
    message.error(String(e))
  }
}
async function updateClear() {
  try {
    upd.value = await api.updateClear()
  } catch (e) {
    message.error(String(e))
  }
}
async function updateApply() {
  busy.value = 'update-apply'
  try {
    const r = await api.updateApply()
    message.success(r.result.message || t('common.done'))
    switching.value = true
    reloadOnReconnect.value = true
    window.setTimeout(() => {
      if (switching.value) {
        switching.value = false
        load(true)
        loadUpdate()
      }
    }, 90000)
  } catch (e) {
    message.error(String(e))
    loadUpdate()
  } finally {
    busy.value = null
  }
}
onUnmounted(() => {
  if (dlTimer) window.clearInterval(dlTimer)
})
// 下载进度与检查结果通过状态流推送
watch(() => status.value?.update, () => loadUpdate(), { deep: true })

const isWindows = computed(() => st.value?.os === 'windows')
const osName = (os: string) => ({ darwin: 'macOS', windows: 'Windows', linux: 'Linux' })[os] ?? os
const isLocal = computed(() => status.value?.source === 'local')
const needsUAC = computed(() => !!st.value && !st.value.elevated)

async function load(quiet = false) {
  try {
    st.value = await api.deployStatus()
  } catch (e) {
    if (!quiet && !switching.value) message.error(String(e))
  }
}

async function run(action: DeployAction) {
  busy.value = action
  lastMessage.value = ''
  try {
    const r = await api.deployAction(action)
    lastMessage.value = r.result.message
    if (r.result.ok) message.success(r.result.message || t('common.done'))
    else message.error(r.result.message || t('common.failed'))
    st.value = { ...r.status, mode: st.value?.mode ?? '', hasShell: st.value?.hasShell ?? false }
    if (r.result.ok && switchingActions.has(action)) {
      // core 即将换进程，等状态流重新连上再刷新
      switching.value = true
      window.setTimeout(() => {
        if (switching.value) {
          switching.value = false
          load(true)
        }
      }, 60000)
      return
    }
  } catch (e) {
    if (!switching.value) message.error(String(e))
  } finally {
    busy.value = null
  }
  load(true)
}

function tag(ok: boolean, yes = t('common.installed'), no = t('common.notInstalled')) {
  return { type: ok ? ('success' as const) : ('default' as const), label: ok ? yes : no }
}

onMounted(() => {
  load()
  loadUpdate()
})
// core 在服务与单进程之间切换后重新读取检测结果
watch(() => status.value?.mode, (m, old) => {
  if (m && old && m !== old) {
    switching.value = false
    load(true)
  }
})
// 状态流断开后重新连上，说明新 core 已就绪；更新后整页重载以拿到新前端
watch(connected, (c, old) => {
  if (c && !old && switching.value) {
    switching.value = false
    if (reloadOnReconnect.value) {
      window.location.reload()
      return
    }
    load(true)
    loadUpdate()
  }
})
</script>

<template>
  <NSpace vertical :size="16">
    <NAlert v-if="st?.os === 'darwin'" type="info">{{ t('deploy.macos') }}</NAlert>
    <NAlert v-else-if="st && !isWindows" type="info">{{ t('deploy.unix', { os: osName(st.os) }) }}</NAlert>
    <NAlert v-if="needsUAC && isWindows" type="warning">
      {{ t('deploy.notElevated') }}
    </NAlert>
    <NAlert v-if="!isLocal" type="info">{{ t('deploy.remote') }}</NAlert>
    <NAlert v-if="switching" type="warning">{{ t('deploy.switching') }}</NAlert>
    <NAlert v-else-if="st?.mode === 'service'" type="info">{{ t('deploy.serviceMode') }}</NAlert>

    <NCard v-if="st" :title="t('deploy.env')">
      <NDescriptions :column="2" label-placement="left" size="small" bordered>
        <NDescriptionsItem :label="t('deploy.os')">{{ osName(st.os) }}</NDescriptionsItem>
        <NDescriptionsItem :label="t('deploy.mode')">{{ tOr(`mode.${st.mode}`, st.mode) }}{{ st.hasShell ? t('deploy.withTray') : '' }}</NDescriptionsItem>
        <NDescriptionsItem :label="t('deploy.admin')">
          <NTag size="small" :type="st.elevated ? 'success' : 'default'">{{ st.elevated ? t('deploy.elevated') : t('deploy.notElevatedTag') }}</NTag>
        </NDescriptionsItem>
        <NDescriptionsItem :label="t('deploy.exePath')">{{ st.exePath }}</NDescriptionsItem>
      </NDescriptions>
    </NCard>

    <NCard :title="t('deploy.update')">
      <template #header-extra>
        <NSpace :size="8" align="center">
          <NText depth="3" style="font-size: 12px">{{ t('deploy.current', { v: upd?.current || status?.version || '' }) }}</NText>
          <NButton size="small" :loading="upd?.checking" @click="updateCheck">{{ t('deploy.checkUpdate') }}</NButton>
        </NSpace>
      </template>
      <NText v-if="upd?.error" type="error" style="display: block; margin-bottom: 8px; font-size: 12px">{{ upd.error }}</NText>
      <template v-if="upd?.latest">
        <NDescriptions :column="2" label-placement="left" size="small" bordered>
          <NDescriptionsItem :label="t('deploy.latest')">
            {{ upd.latest.version }}
            <NTag size="small" :type="upd.available ? 'warning' : 'success'" style="margin-left: 6px">{{ upd.available ? t('deploy.updatable') : t('deploy.upToDate') }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem :label="t('deploy.published')">{{ upd.latest.publishedAt ? formatTime(upd.latest.publishedAt) : '-' }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('deploy.size')">{{ upd.latest.size ? formatBytes(upd.latest.size) : '-' }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('deploy.checkedAt')">{{ upd.lastCheck ? formatTime(upd.lastCheck) : '-' }}</NDescriptionsItem>
        </NDescriptions>
        <pre v-if="upd.latest.notes" class="notes">{{ upd.latest.notes }}</pre>
      </template>
      <NText v-else depth="3">{{ t('deploy.notChecked') }}</NText>
      <NProgress v-if="upd?.downloading" type="line" :percentage="pct" indicator-placement="inside" processing style="margin-top: 12px" />
      <NText v-if="upd?.downloaded && !upd.downloading" depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        {{ t('deploy.downloaded', { v: upd.downloaded.version, size: formatBytes(upd.downloaded.size) }) }}
      </NText>
      <NSpace style="margin-top: 12px">
        <NButton v-if="canDownload" type="primary" @click="updateDownload">{{ t('deploy.downloadV', { v: upd?.latest?.version ?? '' }) }}</NButton>
        <NButton v-if="upd?.downloading" @click="updateCancel">{{ t('deploy.cancelDownload') }}</NButton>
        <NPopconfirm v-if="upd?.downloaded && !upd.downloading" @positive-click="updateApply">
          <template #trigger>
            <NButton type="primary" :loading="busy === 'update-apply'">{{ t('deploy.applyV', { v: upd!.downloaded!.version }) }}</NButton>
          </template>
          {{ t('deploy.applyConfirm', { what: st?.mode === 'service' ? t('deploy.whatService') : t('deploy.whatProgram') }) }}{{ needsUAC ? t('deploy.applyUAC') : '' }}{{ t('deploy.continue') }}
        </NPopconfirm>
        <NButton v-if="upd?.downloaded && !upd.downloading" @click="updateClear">{{ t('deploy.deleteDownloaded') }}</NButton>
      </NSpace>
    </NCard>

    <NCard v-if="st?.task.supported" :title="t('deploy.task')">
      <template #header-extra>
        <NTag size="small" :type="tag(st!.task.installed).type">{{ tag(st!.task.installed).label }}</NTag>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">
        {{ t('deploy.taskHint') }}
        <template v-if="st!.task.installed">{{ t('deploy.taskLevel', { l: st!.task.runLevel === 'Highest' ? t('deploy.highest') : t('deploy.normal') }) }}</template>
        <template v-if="!st!.elevated">{{ t('deploy.taskNotElevated') }}</template>
      </NText>
      <NSpace>
        <NButton type="primary" :loading="busy === 'task-install'" @click="run('task-install')">{{ st!.task.installed ? t('common.reinstall') : t('common.install') }}</NButton>
        <NButton v-if="st!.task.installed" :loading="busy === 'task-remove'" @click="run('task-remove')">{{ t('common.uninstall') }}</NButton>
      </NSpace>
    </NCard>

    <NCard v-if="st?.service.supported" :title="t('deploy.service')">
      <template #header-extra>
        <NSpace>
          <NTag size="small" :type="tag(st!.service.installed).type">{{ tag(st!.service.installed).label }}</NTag>
          <NTag v-if="st!.service.installed" size="small" :type="st!.service.running ? 'success' : 'warning'">{{ st!.service.running ? t('deploy.running') : t('common.stopped') }}</NTag>
        </NSpace>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">
        {{ t('deploy.serviceHint') }}
        <NText type="warning">{{ t('common.needsAdmin') }}</NText>
      </NText>
      <NText v-if="st!.service.binary" depth="3" style="display: block; margin-bottom: 12px; font-size: 12px">{{ t('deploy.serviceBinary', { p: st!.service.binary }) }}</NText>
      <NSpace>
        <NPopconfirm v-if="!st!.service.installed" @positive-click="run('service-install')">
          <template #trigger>
            <NButton type="primary" :loading="busy === 'service-install'">{{ t('deploy.installStart') }}</NButton>
          </template>
          {{ t('deploy.installConfirm') }}
        </NPopconfirm>
        <template v-else>
          <NButton v-if="!st!.service.running" type="primary" :loading="busy === 'service-start'" @click="run('service-start')">{{ t('common.start') }}</NButton>
          <NButton v-else :loading="busy === 'service-stop'" @click="run('service-stop')">{{ t('common.stop') }}</NButton>
          <NPopconfirm @positive-click="run('service-remove')">
            <template #trigger>
              <NButton type="error" ghost :loading="busy === 'service-remove'">{{ t('common.uninstall') }}</NButton>
            </template>
            {{ t('deploy.removeConfirm') }}
          </NPopconfirm>
        </template>
      </NSpace>
      <NText v-if="st!.service.detail" type="error" style="display: block; margin-top: 8px; font-size: 12px">{{ st!.service.detail }}</NText>
    </NCard>

    <NCard v-if="st?.firewall.supported" :title="t('deploy.firewall')">
      <template #header-extra>
        <NTag size="small" :type="tag(st!.firewall.installed).type">{{ tag(st!.firewall.installed).label }}</NTag>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">
        {{ t('deploy.firewallHint', { p: st!.firewall.udpPort }) }} <NText type="warning">{{ t('common.needsAdmin') }}</NText>
      </NText>
      <NSpace>
        <NButton type="primary" :loading="busy === 'firewall-add'" @click="run('firewall-add')">{{ st!.firewall.installed ? t('deploy.readd') : t('common.add') }}</NButton>
        <NButton v-if="st!.firewall.installed" :loading="busy === 'firewall-remove'" @click="run('firewall-remove')">{{ t('common.delete') }}</NButton>
      </NSpace>
    </NCard>

    <NCard v-if="st?.webview2.supported" :title="t('deploy.webview2')">
      <template #header-extra>
        <NTag size="small" :type="tag(st!.webview2.installed).type">{{ st!.webview2.installed ? t('deploy.webview2Installed', { v: st!.webview2.version ?? '' }) : t('common.notInstalled') }}</NTag>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">{{ t('deploy.webview2Hint') }}</NText>
      <NButton v-if="!st!.webview2.installed" type="primary" :loading="busy === 'webview2-install'" @click="run('webview2-install')">{{ t('deploy.downloadInstall') }}</NButton>
    </NCard>

    <NCard v-if="st?.autoLogon.supported" :title="t('deploy.autoLogon')">
      <template #header-extra>
        <NTag size="small" :type="st!.autoLogon.enabled ? 'success' : 'default'">{{ st!.autoLogon.enabled ? t('deploy.autoLogonOn', { u: st!.autoLogon.user ?? '' }) : t('deploy.autoLogonOff') }}</NTag>
      </template>
      <NText depth="3">
        {{ t('deploy.autoLogonHint1') }}
        {{ t('deploy.autoLogonHint2') }}
      </NText>
    </NCard>

    <NText v-if="lastMessage" depth="3">{{ t('deploy.lastResult', { m: lastMessage }) }}</NText>
  </NSpace>
</template>

<style scoped>
.notes {
  margin: 10px 0 0;
  padding: 8px 10px;
  font-size: 12px;
  white-space: pre-wrap;
  background: rgba(128, 128, 128, 0.1);
  border-radius: 4px;
}
</style>
