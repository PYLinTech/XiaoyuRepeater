<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BrandMark from '../components/BrandMark.vue'
import Icon from '../components/Icon.vue'
import { api, ApiError } from '../api'
import { authState, refreshSession } from '../auth'

const router = useRouter()
const route = useRoute()

const setupMode = ref(authState.setupRequired)
const password = ref('')
const confirmPassword = ref('')
const showPassword = ref(false)
const busy = ref(false)
const error = ref('')

const title = computed(() => (setupMode.value ? '设置管理密码' : '登录控制台'))
const description = computed(() =>
  setupMode.value
    ? '首次使用，请设置一个用于进入控制台的管理密码。'
    : '输入管理密码，进入 XiaoyuRepeater 控制台。',
)

function nextPath(): string {
  const raw = route.query.next
  if (typeof raw === 'string' && raw.startsWith('/') && !raw.startsWith('//')) return raw
  return '/'
}

async function submit() {
  if (busy.value) return
  error.value = ''
  if (!password.value) {
    error.value = '请输入密码'
    return
  }
  if (setupMode.value) {
    if (password.value.length < 8) {
      error.value = '密码至少需要 8 个字符'
      return
    }
    if (password.value !== confirmPassword.value) {
      error.value = '两次输入的密码不一致'
      return
    }
  }
  busy.value = true
  try {
    if (setupMode.value) {
      await api('/api/admin/setup', { body: { password: password.value } })
    } else {
      await api('/api/admin/login', { body: { password: password.value } })
    }
    await refreshSession()
    await router.replace(nextPath())
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      setupMode.value = true
      error.value = '请先设置管理密码'
    } else {
      error.value = err instanceof ApiError ? err.message : '操作失败，请稍后重试'
    }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-glow" />
    <div class="login-grid" />
    <div class="login-wrap">
      <div class="login-card">
        <div class="login-brand">
          <BrandMark :size="42" />
          <div>
            <div class="login-brand-name">XiaoyuRepeater</div>
            <div class="login-brand-sub">REPEATER CONSOLE</div>
          </div>
        </div>

        <h1 class="login-title">{{ title }}</h1>
        <p class="login-desc">{{ description }}</p>

        <form class="login-form" @submit.prevent="submit">
          <div class="field">
            <label class="field-label" for="password">{{ setupMode ? '新密码' : '管理密码' }}</label>
            <div class="input-wrap">
              <input
                id="password"
                v-model="password"
                class="input login-input"
                :type="showPassword ? 'text' : 'password'"
                :autocomplete="setupMode ? 'new-password' : 'current-password'"
                :placeholder="setupMode ? '至少 8 个字符' : '请输入管理密码'"
                autofocus
              />
              <button
                class="input-affix"
                type="button"
                :aria-label="showPassword ? '隐藏密码' : '显示密码'"
                :title="showPassword ? '隐藏密码' : '显示密码'"
                @click="showPassword = !showPassword"
              >
                <Icon :name="showPassword ? 'eye-off' : 'eye'" :size="17" />
              </button>
            </div>
          </div>

          <div v-if="setupMode" class="field">
            <label class="field-label" for="confirm-password">确认密码</label>
            <input
              id="confirm-password"
              v-model="confirmPassword"
              class="input login-input"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="new-password"
              placeholder="再次输入密码"
            />
          </div>

          <div v-if="error" class="login-error">
            <Icon name="alert" :size="15" />
            <span>{{ error }}</span>
          </div>

          <button class="btn btn-primary login-submit" type="submit" :disabled="busy">
            <span v-if="busy" class="spinner" />
            {{ setupMode ? '完成设置' : '进入控制台' }}
          </button>
        </form>

        <div class="login-foot">
          <span class="foot-status">
            <span class="dot online" />
            实时中继
          </span>
          <span class="foot-version">v{{ authState.version || '0.1.0' }}</span>
        </div>
      </div>
      <p class="login-note">会议分发 · 远程协助 · 实时双向转发</p>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  position: relative;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 28px 18px;
  overflow: hidden;
  background: var(--bg);
}

.login-glow {
  position: absolute;
  top: -240px;
  left: 50%;
  width: 720px;
  height: 520px;
  transform: translateX(-50%);
  background: radial-gradient(closest-side, rgba(73, 217, 167, 0.1), transparent 72%);
  pointer-events: none;
}

.login-grid {
  position: absolute;
  inset: 0;
  background-image: linear-gradient(rgba(255, 255, 255, 0.028) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255, 255, 255, 0.028) 1px, transparent 1px);
  background-size: 52px 52px;
  mask-image: radial-gradient(ellipse 70% 60% at 50% 30%, #000 20%, transparent 75%);
  -webkit-mask-image: radial-gradient(ellipse 70% 60% at 50% 30%, #000 20%, transparent 75%);
  pointer-events: none;
}

.login-wrap {
  position: relative;
  width: min(400px, 100%);
}

.login-card {
  padding: 30px 30px 24px;
  background: #191919;
  border: 1px solid #2f2f2f;
  border-radius: 18px;
  box-shadow: 0 30px 80px rgba(0, 0, 0, 0.55);
}

.login-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 30px;
}

.login-brand-name {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.login-brand-sub {
  margin-top: 2px;
  font-size: 9px;
  letter-spacing: 0.16em;
  color: var(--text-3);
}

.login-title {
  font-size: 23px;
  font-weight: 600;
  letter-spacing: 0.01em;
}

.login-desc {
  margin-top: 8px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-2);
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: 15px;
  margin-top: 24px;
}

.login-input {
  height: 42px;
}

.login-submit {
  height: 42px;
  margin-top: 3px;
  font-size: 14px;
}

.login-error {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 9px 11px;
  border-radius: 8px;
  background: var(--danger-soft);
  border: 1px solid var(--danger-line);
  color: var(--danger);
  font-size: 12.5px;
  line-height: 1.5;
}

.login-error :deep(svg) {
  flex-shrink: 0;
  margin-top: 1px;
}

.login-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 26px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
  font-size: 11.5px;
  color: var(--text-3);
}

.foot-status {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}

.foot-version {
  font-family: var(--mono);
}

.login-note {
  margin-top: 18px;
  text-align: center;
  font-size: 11.5px;
  letter-spacing: 0.04em;
  color: #5d5d5d;
}
</style>
