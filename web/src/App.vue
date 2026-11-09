<script setup lang="ts">
import { onMounted, ref } from 'vue'
import Shell from './Shell.vue'
import Login from './pages/Login.vue'
import { api } from './api'
import { needLogin, sessionInfo } from './session'

// 先确认是否需要登录，再挂载主界面（主界面一挂载就会连 /ws/events）。
const ready = ref(false)
onMounted(async () => {
  try {
    const s = await api.session()
    sessionInfo.value = s
    needLogin.value = !s.authenticated
  } catch {
    // 连不上时照常显示主界面，由各页面提示错误
  }
  ready.value = true
})
</script>

<template>
  <template v-if="ready">
    <Login v-if="needLogin" />
    <Shell v-else />
  </template>
</template>
