<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { NResult } from 'naive-ui'
import { routeTitle } from '../router'
import { t } from '../i18n'

const route = useRoute()
const title = computed(() => routeTitle(route.meta))
const phase = computed(() => route.meta.phase as number | undefined)
</script>

<template>
  <NResult status="info" :title="title" :description="phase ? t('placeholder.phase', { n: phase }) : t('placeholder.todo')" />
</template>
