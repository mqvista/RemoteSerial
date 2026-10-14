<script setup lang="ts">
import { computed, h } from 'vue'
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
</script>

<template>
  <NConfigProvider :locale="zhCN" :date-locale="dateZhCN">
    <NMessageProvider>
      <NDialogProvider>
        <NLayout has-sider style="height: 100vh">
          <NLayoutSider bordered :width="200" content-style="padding: 12px 0">
            <div class="brand">
              <NText strong>rsagent</NText>
              <NText depth="3" style="font-size: 12px">远程串口桥接</NText>
            </div>
            <NMenu :value="activeKey" :options="menuOptions" />
          </NLayoutSider>
          <NLayout>
            <NLayoutHeader bordered class="header">
              <NText strong style="font-size: 16px">{{ pageTitle }}</NText>
            </NLayoutHeader>
            <NLayoutContent content-style="padding: 20px">
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
code {
  background: rgba(128, 128, 128, 0.15);
  padding: 1px 5px;
  border-radius: 3px;
}
</style>
