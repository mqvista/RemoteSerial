<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NCard, NDescriptions, NDescriptionsItem, NPopconfirm, NProgress, NSpace, NTag, NText, useMessage } from 'naive-ui'
import { api, formatBytes } from '../api'
import type { DeployAction, DeployStatus, UpdateStatus } from '../api'
import { useStatus } from '../status'

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
    if (upd.value.available) message.info(`有新版本 ${upd.value.latest?.version}`)
    else message.success('已是最新版本')
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
        else if (upd.value?.downloaded) message.success(`已下载 ${upd.value.downloaded.version}`)
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
    message.success(r.result.message || '完成')
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
    if (r.result.ok) message.success(r.result.message || '完成')
    else message.error(r.result.message || '失败')
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

function tag(ok: boolean, yes = '已安装', no = '未安装') {
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
    <NAlert v-if="st && !isWindows" type="info">当前为 {{ st.os }}，桌面壳与计划任务仅 Windows 支持。Linux 站点用"系统服务"把 core 注册为 systemd 服务。</NAlert>
    <NAlert v-if="needsUAC && isWindows" type="warning">
      当前进程未提权。标有"需管理员"的动作会弹出 UAC 确认框，确认框出现在远端的屏幕上。远端无人时，请先在现场安装带"最高权限"的开机自启任务，之后程序本身就是提权状态。
    </NAlert>
    <NAlert v-if="!isLocal" type="info">你正通过 tailnet 远程操作。安装、启动、停止、卸载服务都会切换 core 所在进程，连接会中断几秒后自动恢复。</NAlert>
    <NAlert v-if="switching" type="warning">core 正在切换进程，请稍候，界面会自动恢复。</NAlert>
    <NAlert v-else-if="st?.mode === 'service'" type="info">core 正以服务运行。停止或卸载服务后，远端托盘程序会自动在本进程重新启动 core。</NAlert>

    <NCard v-if="st" title="运行环境">
      <NDescriptions :column="2" label-placement="left" size="small" bordered>
        <NDescriptionsItem label="系统">{{ st.os }}</NDescriptionsItem>
        <NDescriptionsItem label="运行模式">{{ st.mode }}{{ st.hasShell ? '（带托盘）' : '' }}</NDescriptionsItem>
        <NDescriptionsItem label="管理员权限">
          <NTag size="small" :type="st.elevated ? 'success' : 'default'">{{ st.elevated ? '已提权' : '未提权' }}</NTag>
        </NDescriptionsItem>
        <NDescriptionsItem label="程序路径">{{ st.exePath }}</NDescriptionsItem>
      </NDescriptions>
    </NCard>

    <NCard title="更新">
      <template #header-extra>
        <NSpace :size="8" align="center">
          <NText depth="3" style="font-size: 12px">当前 {{ upd?.current || status?.version }}</NText>
          <NButton size="small" :loading="upd?.checking" @click="updateCheck">检查更新</NButton>
        </NSpace>
      </template>
      <NText v-if="upd?.error" type="error" style="display: block; margin-bottom: 8px; font-size: 12px">{{ upd.error }}</NText>
      <template v-if="upd?.latest">
        <NDescriptions :column="2" label-placement="left" size="small" bordered>
          <NDescriptionsItem label="最新版本">
            {{ upd.latest.version }}
            <NTag size="small" :type="upd.available ? 'warning' : 'success'" style="margin-left: 6px">{{ upd.available ? '可更新' : '已是最新' }}</NTag>
          </NDescriptionsItem>
          <NDescriptionsItem label="发布时间">{{ upd.latest.publishedAt ? new Date(upd.latest.publishedAt).toLocaleString('zh-CN', { hour12: false }) : '-' }}</NDescriptionsItem>
          <NDescriptionsItem label="大小">{{ upd.latest.size ? formatBytes(upd.latest.size) : '-' }}</NDescriptionsItem>
          <NDescriptionsItem label="检查时间">{{ upd.lastCheck ? new Date(upd.lastCheck).toLocaleString('zh-CN', { hour12: false }) : '-' }}</NDescriptionsItem>
        </NDescriptions>
        <pre v-if="upd.latest.notes" class="notes">{{ upd.latest.notes }}</pre>
      </template>
      <NText v-else depth="3">尚未检查。清单地址在“设置”页配置，默认指向 GitHub Releases。</NText>
      <NProgress v-if="upd?.downloading" type="line" :percentage="pct" indicator-placement="inside" processing style="margin-top: 12px" />
      <NText v-if="upd?.downloaded && !upd.downloading" depth="3" style="display: block; margin-top: 8px; font-size: 12px">
        已下载 {{ upd.downloaded.version }}（{{ formatBytes(upd.downloaded.size) }}），SHA-256 校验通过。
      </NText>
      <NSpace style="margin-top: 12px">
        <NButton v-if="canDownload" type="primary" @click="updateDownload">下载 {{ upd?.latest?.version }}</NButton>
        <NButton v-if="upd?.downloading" @click="updateCancel">取消下载</NButton>
        <NPopconfirm v-if="upd?.downloaded && !upd.downloading" @positive-click="updateApply">
          <template #trigger>
            <NButton type="primary" :loading="busy === 'update-apply'">应用 {{ upd!.downloaded!.version }} 并重启</NButton>
          </template>
          替换程序文件并重启{{ st?.mode === 'service' ? '服务' : '程序' }}，连接会中断几秒后自动恢复。{{ needsUAC ? '程序目录不可写时会弹 UAC。' : '' }}继续？
        </NPopconfirm>
        <NButton v-if="upd?.downloaded && !upd.downloading" @click="updateClear">删除已下载</NButton>
      </NSpace>
    </NCard>

    <NCard v-if="st?.task.supported" title="开机自启（计划任务）">
      <template #header-extra>
        <NTag size="small" :type="tag(st!.task.installed).type">{{ tag(st!.task.installed).label }}</NTag>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">
        用户登录时启动，每 5 分钟检查一次，已在运行则不重复启动，兼作崩溃后自动拉起。配合自动登录实现无人值守。
        <template v-if="st!.task.installed">当前权限级别：{{ st!.task.runLevel === 'Highest' ? '最高权限' : '普通权限' }}。</template>
        <template v-if="!st!.elevated">现在安装会以普通权限注册；若需最高权限，请以管理员身份运行程序后再安装。</template>
      </NText>
      <NSpace>
        <NButton type="primary" :loading="busy === 'task-install'" @click="run('task-install')">{{ st!.task.installed ? '重新安装' : '安装' }}</NButton>
        <NButton v-if="st!.task.installed" :loading="busy === 'task-remove'" @click="run('task-remove')">卸载</NButton>
      </NSpace>
    </NCard>

    <NCard v-if="st?.service.supported" title="系统服务">
      <template #header-extra>
        <NSpace>
          <NTag size="small" :type="tag(st!.service.installed).type">{{ tag(st!.service.installed).label }}</NTag>
          <NTag v-if="st!.service.installed" size="small" :type="st!.service.running ? 'success' : 'warning'">{{ st!.service.running ? '运行中' : '已停止' }}</NTag>
        </NSpace>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">
        把 core 注册为系统服务，开机即运行，不依赖用户登录。适合不允许自动登录的站点。安装时会把程序复制到数据目录的 bin 下并用副本注册，更新程序后重新点一次安装。安装后托盘程序只负责界面，退出托盘不影响服务。
        <NText type="warning">需管理员</NText>
      </NText>
      <NText v-if="st!.service.binary" depth="3" style="display: block; margin-bottom: 12px; font-size: 12px">服务程序：{{ st!.service.binary }}</NText>
      <NSpace>
        <NPopconfirm v-if="!st!.service.installed" @positive-click="run('service-install')">
          <template #trigger>
            <NButton type="primary" :loading="busy === 'service-install'">安装并启动</NButton>
          </template>
          安装服务后本进程的 core 会停止，由服务接管。继续？
        </NPopconfirm>
        <template v-else>
          <NButton v-if="!st!.service.running" type="primary" :loading="busy === 'service-start'" @click="run('service-start')">启动</NButton>
          <NButton v-else :loading="busy === 'service-stop'" @click="run('service-stop')">停止</NButton>
          <NPopconfirm @positive-click="run('service-remove')">
            <template #trigger>
              <NButton type="error" ghost :loading="busy === 'service-remove'">卸载</NButton>
            </template>
            卸载服务后需要重新双击程序运行。继续？
          </NPopconfirm>
        </template>
      </NSpace>
      <NText v-if="st!.service.detail" type="error" style="display: block; margin-top: 8px; font-size: 12px">{{ st!.service.detail }}</NText>
    </NCard>

    <NCard v-if="st?.firewall.supported" title="防火墙规则">
      <template #header-extra>
        <NTag size="small" :type="tag(st!.firewall.installed).type">{{ tag(st!.firewall.installed).label }}</NTag>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">
        放行 UDP {{ st!.firewall.udpPort }} 入站，提高与本地设备直连的成功率。不加也能通过中转工作。<NText type="warning">需管理员</NText>
      </NText>
      <NSpace>
        <NButton type="primary" :loading="busy === 'firewall-add'" @click="run('firewall-add')">{{ st!.firewall.installed ? '重新添加' : '添加' }}</NButton>
        <NButton v-if="st!.firewall.installed" :loading="busy === 'firewall-remove'" @click="run('firewall-remove')">删除</NButton>
      </NSpace>
    </NCard>

    <NCard v-if="st?.webview2.supported" title="WebView2 运行时">
      <template #header-extra>
        <NTag size="small" :type="tag(st!.webview2.installed).type">{{ st!.webview2.installed ? `已安装 ${st!.webview2.version}` : '未安装' }}</NTag>
      </template>
      <NText depth="3" style="display: block; margin-bottom: 12px">窗口模式依赖 WebView2。缺失时程序会改用系统浏览器打开界面。安装会下载微软 Evergreen 引导程序并运行，引导程序自行请求提权。</NText>
      <NButton v-if="!st!.webview2.installed" type="primary" :loading="busy === 'webview2-install'" @click="run('webview2-install')">下载并安装</NButton>
    </NCard>

    <NCard v-if="st?.autoLogon.supported" title="自动登录">
      <template #header-extra>
        <NTag size="small" :type="st!.autoLogon.enabled ? 'success' : 'default'">{{ st!.autoLogon.enabled ? `已启用：${st!.autoLogon.user}` : '未启用' }}</NTag>
      </template>
      <NText depth="3">
        计划任务路线需要远端重启后自动回到桌面。请用微软 Sysinternals 的 Autologon 工具配置，它把密码存为 LSA secret 而不是注册表明文。本程序不代存密码。
        现场有人经过时，再加一个登录后自动锁屏的计划任务，锁屏不影响托盘程序运行。
      </NText>
    </NCard>

    <NText v-if="lastMessage" depth="3">最近结果：{{ lastMessage }}</NText>
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
