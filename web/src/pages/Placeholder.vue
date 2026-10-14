<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { NResult } from 'naive-ui'

const route = useRoute()
const title = computed(() => (route.meta.title as string | undefined) ?? '')
const phase = computed(() => route.meta.phase as number | undefined)
</script>

<template>
  <NResult status="info" :title="title" :description="phase ? `第 ${phase} 期实现，见 docs/开发方案与计划.md` : '待实现'" />
</template>
