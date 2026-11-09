<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NCard, NConfigProvider, NInput, NSelect, NSpace, NText } from 'naive-ui'
import { api } from '../api'
import { needLogin, sessionInfo } from '../session'
import { locale, localeOptions, naiveDateLocale, naiveLocale, t } from '../i18n'

// 局域网访问的登录页：LuCI 的“打开管理界面”会带一次性令牌直接登录，这里是密码登录的入口。
const password = ref('')
const busy = ref(false)
const error = ref('')
const passwordSet = computed(() => sessionInfo.value?.passwordSet ?? false)

async function submit() {
  if (!password.value) return
  busy.value = true
  error.value = ''
  try {
    await api.login(password.value)
    sessionInfo.value = await api.session()
    needLogin.value = false
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <NConfigProvider :locale="naiveLocale" :date-locale="naiveDateLocale">
    <div class="login-wrap">
      <NCard class="login-card" :title="t('login.title')">
        <template #header-extra>
          <NSelect v-model:value="locale" :options="localeOptions" size="small" style="width: 120px" :aria-label="t('lang.label')" />
        </template>
        <NSpace vertical :size="14">
          <NText depth="3">{{ t('login.intro') }}</NText>
          <template v-if="passwordSet">
            <NInput
              v-model:value="password"
              type="password"
              show-password-on="click"
              :placeholder="t('login.password')"
              :input-props="{ autocomplete: 'current-password' }"
              autofocus
              @keyup.enter="submit"
            />
            <NButton type="primary" block :loading="busy" :disabled="!password" @click="submit">{{ t('login.submit') }}</NButton>
          </template>
          <NAlert v-else type="info" :show-icon="false">{{ t('login.noPassword') }}</NAlert>
          <NAlert v-if="error" type="error" :show-icon="false">{{ error }}</NAlert>
          <NText depth="3" style="font-size: 12px">{{ t('login.luciHint') }}</NText>
        </NSpace>
      </NCard>
    </div>
  </NConfigProvider>
</template>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  box-sizing: border-box;
}
.login-card {
  width: 100%;
  max-width: 400px;
}
</style>
