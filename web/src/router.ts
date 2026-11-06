import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { watch } from 'vue'
import Overview from './pages/Overview.vue'
import Tailscale from './pages/Tailscale.vue'
import Bridges from './pages/Bridges.vue'
import Routes from './pages/Routes.vue'
import TerminalPage from './pages/Terminal.vue'
import Logs from './pages/Logs.vue'
import Deploy from './pages/Deploy.vue'
import Settings from './pages/Settings.vue'
import { locale, t } from './i18n'
import type { MessageKey } from './i18n'

// 页面清单对应方案文档 4.3 节。
export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'overview', component: Overview, meta: { titleKey: 'nav.overview' } },
  { path: '/tailscale', name: 'tailscale', component: Tailscale, meta: { titleKey: 'nav.tailscale' } },
  { path: '/bridges', name: 'bridges', component: Bridges, meta: { titleKey: 'nav.bridges' } },
  { path: '/routes', name: 'routes', component: Routes, meta: { titleKey: 'nav.routes' } },
  { path: '/terminal', name: 'terminal', component: TerminalPage, meta: { titleKey: 'nav.terminal' } },
  { path: '/logs', name: 'logs', component: Logs, meta: { titleKey: 'nav.logs' } },
  { path: '/deploy', name: 'deploy', component: Deploy, meta: { titleKey: 'nav.deploy' } },
  { path: '/settings', name: 'settings', component: Settings, meta: { titleKey: 'nav.settings' } },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

/** 路由的页面标题，随语言切换。 */
export function routeTitle(meta: Record<string | number | symbol, unknown>): string {
  const k = meta.titleKey as MessageKey | undefined
  return k ? t(k) : ''
}

function updateDocTitle() {
  const title = routeTitle(router.currentRoute.value.meta)
  document.title = title ? `${title} · rsagent` : 'rsagent'
}
router.afterEach(updateDocTitle)
watch(locale, updateDocTitle)
