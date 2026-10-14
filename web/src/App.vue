<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref } from 'vue'
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
  NText,
  zhCN,
  dateZhCN,
} from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import { routes } from './router'

const route = useRoute()

const menuOptions = computed<MenuOption[]>(() =>
  routes.map((r) => ({
    key: r.name as string,
    label: () => h(RouterLink, { to: r.path }, { default: () => r.meta?.title as string }),
  })),
)

const activeKey = computed(() => route.name as string)
const pageTitle = computed(() => (route.meta.title as string | undefined) ?? '')

// 窗口较窄时收起侧栏，菜单改为顶部横向，内容区留出整个宽度。
const narrowQuery = window.matchMedia('(max-width: 820px)')
const narrow = ref(narrowQuery.matches)
const onNarrowChange = (e: MediaQueryListEvent) => (narrow.value = e.matches)
onMounted(() => narrowQuery.addEventListener('change', onNarrowChange))
onUnmounted(() => narrowQuery.removeEventListener('change', onNarrowChange))
</script>

<template>
  <NConfigProvider :locale="zhCN" :date-locale="dateZhCN">
    <NMessageProvider>
      <NDialogProvider>
        <NLayout has-sider style="height: 100vh">
          <NLayoutSider v-if="!narrow" bordered :width="200" content-style="padding: 12px 0">
            <div class="brand">
              <NText strong>rsagent</NText>
              <NText depth="3" style="font-size: 12px">远程串口桥接</NText>
            </div>
            <NMenu :value="activeKey" :options="menuOptions" />
          </NLayoutSider>
          <NLayout>
            <NLayoutHeader bordered class="header" :class="{ narrow }">
              <template v-if="narrow">
                <div class="header-row">
                  <NText strong>rsagent</NText>
                  <NText depth="3" style="font-size: 12px">{{ pageTitle }}</NText>
                </div>
                <NMenu :value="activeKey" :options="menuOptions" mode="horizontal" responsive />
              </template>
              <NText v-else strong style="font-size: 16px">{{ pageTitle }}</NText>
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
  align-items: baseline;
  gap: 10px;
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
