import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import Overview from './pages/Overview.vue'
import Tailscale from './pages/Tailscale.vue'
import Bridges from './pages/Bridges.vue'
import Routes from './pages/Routes.vue'
import TerminalPage from './pages/Terminal.vue'
import Logs from './pages/Logs.vue'
import Deploy from './pages/Deploy.vue'
import Settings from './pages/Settings.vue'
import Placeholder from './pages/Placeholder.vue'

// 页面清单对应方案文档 4.3 节。未实现的页面用占位页标明期数。
export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'overview', component: Overview, meta: { title: '概览' } },
  { path: '/tailscale', name: 'tailscale', component: Tailscale, meta: { title: 'Tailscale' } },
  { path: '/bridges', name: 'bridges', component: Bridges, meta: { title: '串口桥接' } },
  { path: '/routes', name: 'routes', component: Routes, meta: { title: '子网路由' } },
  { path: '/terminal', name: 'terminal', component: TerminalPage, meta: { title: '终端' } },
  { path: '/logs', name: 'logs', component: Logs, meta: { title: '日志' } },
  { path: '/deploy', name: 'deploy', component: Deploy, meta: { title: '部署' } },
  { path: '/settings', name: 'settings', component: Settings, meta: { title: '设置' } },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.afterEach((to) => {
  const title = (to.meta.title as string | undefined) ?? ''
  document.title = title ? `${title} · rsagent` : 'rsagent'
})
