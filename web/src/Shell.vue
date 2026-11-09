<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import {
  NConfigProvider,
  NDialogProvider,
  NLayout,
  NLayoutSider,
  NLayoutHeader,
  NLayoutContent,
  NMenu,
  NMessageProvider,
  NSelect,
  NText,
  NButton,
} from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import { routes, routeTitle } from './router'
import { locale, localeOptions, naiveDateLocale, naiveLocale, t } from './i18n'
import type { Locale } from './i18n'
import { api } from './api'
import { useStatus } from './status'
import { needLogin, sessionInfo } from './session'

const route = useRoute()
const { status } = useStatus()

// 语言以 core 为准：托盘、窗口菜单和后端提示都跟随它。切换时写回 core，所有打开的页面一起切换。
let savingLang: Locale | null = null
watch(
  () => status.value?.language,
  (l) => {
    if ((l === 'en' || l === 'zh-TW') && !savingLang) locale.value = l
  },
  { immediate: true },
)
async function changeLocale(v: Locale) {
  locale.value = v
  savingLang = v
  try {
    await api.saveSettings({ language: v })
  } catch {
    // 远端无权修改设置时只切换本页
  } finally {
    savingLang = null
  }
}

const menuOptions = computed<MenuOption[]>(() =>
  routes.map((r) => ({
    key: r.name as string,
    label: () => h(RouterLink, { to: r.path }, { default: () => routeTitle(r.meta ?? {}) }),
  })),
)

// 局域网登录的会话可以退出；本机与 tailnet 来源不需要登录
const canLogout = computed(() => sessionInfo.value?.source === 'lan')
async function logout() {
  try {
    await api.logout()
  } finally {
    needLogin.value = true
  }
}

const activeKey = computed(() => route.name as string)
const pageTitle = computed(() => routeTitle(route.meta))

// 窗口较窄时收起侧栏，菜单改为顶部横向，内容区留出整个宽度。
const narrowQuery = window.matchMedia('(max-width: 820px)')
const narrow = ref(narrowQuery.matches)
const onNarrowChange = (e: MediaQueryListEvent) => (narrow.value = e.matches)
onMounted(() => narrowQuery.addEventListener('change', onNarrowChange))
onUnmounted(() => narrowQuery.removeEventListener('change', onNarrowChange))
</script>

<template>
  <NConfigProvider :locale="naiveLocale" :date-locale="naiveDateLocale">
    <NMessageProvider>
      <NDialogProvider>
        <NLayout has-sider style="height: 100vh">
          <NLayoutSider v-if="!narrow" bordered :width="200" content-style="padding: 12px 0">
            <div class="brand">
              <NText strong>rsagent</NText>
              <NText depth="3" style="font-size: 12px">{{ t('brand.subtitle') }}</NText>
            </div>
            <NMenu :value="activeKey" :options="menuOptions" />
          </NLayoutSider>
          <NLayout>
            <NLayoutHeader bordered class="header" :class="{ narrow }">
              <template v-if="narrow">
                <div class="header-row">
                  <NText strong>rsagent</NText>
                  <NText depth="3" style="font-size: 12px">{{ pageTitle }}</NText>
                  <NSelect :value="locale" :options="localeOptions" @update:value="changeLocale" size="tiny" class="lang" :aria-label="t('lang.label')" />
                  <NButton v-if="canLogout" size="tiny" quaternary @click="logout">{{ t('common.logout') }}</NButton>
                </div>
                <NMenu :value="activeKey" :options="menuOptions" mode="horizontal" responsive />
              </template>
              <div v-else class="header-row">
                <NText strong style="font-size: 16px">{{ pageTitle }}</NText>
                <NSelect :value="locale" :options="localeOptions" @update:value="changeLocale" size="small" class="lang" :aria-label="t('lang.label')" />
                <NButton v-if="canLogout" size="small" quaternary @click="logout">{{ t('common.logout') }}</NButton>
              </div>
            </NLayoutHeader>
            <NLayoutContent :content-style="narrow ? 'padding: 12px' : 'padding: 20px'">
              <RouterView />
            </NLayoutContent>
          </NLayout>
        </NLayout>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style>
html,
body,
#app {
  margin: 0;
  height: 100%;
}
.brand {
  display: flex;
  flex-direction: column;
  padding: 4px 24px 12px;
}
.header {
  padding: 12px 20px;
}
.header.narrow {
  padding: 6px 12px 0;
}
.header-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.header-row .lang {
  width: 120px;
  margin-left: auto;
}
code {
  background: rgba(128, 128, 128, 0.15);
  padding: 1px 5px;
  border-radius: 3px;
}
/* 表格单元格不折行，窄窗口下由表格自身横向滚动 */
.n-data-table .n-data-table-td,
.n-data-table .n-data-table-th {
  white-space: nowrap;
}
</style>
