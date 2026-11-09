// 局域网访问的登录状态。本机与 tailnet 来源不需要登录。
import { ref } from 'vue'

export interface SessionInfo {
  source: 'local' | 'tailnet' | 'lan'
  authenticated: boolean
  passwordSet: boolean
  lan: boolean
}

export const sessionInfo = ref<SessionInfo | null>(null)
/** 为 true 时显示登录页；任何接口返回 401 {login: true} 也会把它打开。 */
export const needLogin = ref(false)
