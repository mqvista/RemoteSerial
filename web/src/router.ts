import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import Overview from './pages/Overview.vue'
import Tailscale from './pages/Tailscale.vue'
import Bridges from './pages/Bridges.vue'
import Logs from './pages/Logs.vue'
import Placeholder from './pages/Placeholder.vue'

// 页面清单对应方案文档 4.3 节。未实现的页面用占位页标明期数。
export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'overview', component: Overview, meta: { title: '概览' } },
  { path: '/tailscale', name: 'tailscale', component: Tailscale, meta: { title: 'Tailscale' } },
  { path: '/bridges', name: 'bridges', component: Bridges, meta: { title: '串口桥接' } },
  { path: '/routes', name: 'routes', component: Placeholder, meta: { title: '子网路由', phase: 2 } },
  { path: '/terminal', name: 'terminal', component: Placeholder, meta: { title: '终端', phase: 3 } },
  { path: '/logs', name: 'logs', component: Logs, meta: { title: '日志' } },
  { path: '/deploy', name: 'deploy', component: Placeholder, meta: { title: '部署', phase: 2 } },
  { path: '/settings', name: 'settings', component: Placeholder, meta: { title: '设置', phase: 2 } },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.afterEach((to) => {
  const title = (to.meta.title as string | undefined) ?? ''
  document.title = title ? `${title} · rsagent` : 'rsagent'
})
