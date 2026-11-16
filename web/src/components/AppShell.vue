<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import BrandMark from './BrandMark.vue'
import Icon from './Icon.vue'
import Modal from './Modal.vue'
import { api, ApiError } from '../api'
import { authState, markLoggedOut } from '../auth'
import { toast } from '../toast'

const router = useRouter()

const navItems = [
  { name: 'overview', label: '概览', icon: 'overview' },
  { name: 'rooms', label: '中转房间', icon: 'rooms' },
  { name: 'keys', label: 'API 密钥', icon: 'keys' },
]

const passwordOpen = ref(false)
const passwordBusy = ref(false)
const passwordError = ref('')
const form = reactive({ current: '', next: '', confirm: '' })

function openPassword() {
  passwordError.value = ''
  form.current = ''
  form.next = ''
  form.confirm = ''
  passwordOpen.value = true
}

function closePassword() {
  if (passwordBusy.value) return
  passwordOpen.value = false
}

async function submitPassword() {
  passwordError.value = ''
  if (!form.current) {
    passwordError.value = '请输入当前密码'
    return
  }
  if (form.next.length < 8) {
    passwordError.value = '新密码至少需要 8 个字符'
    return
  }
  if (form.next !== form.confirm) {
    passwordError.value = '两次输入的新密码不一致'
    return
  }
  passwordBusy.value = true
  try {
    await api('/api/admin/password', {
      method: 'PUT',
      body: { current_password: form.current, new_password: form.next },
    })
    passwordOpen.value = false
    toast('管理密码已更新')
  } catch (error) {
    passwordError.value = error instanceof ApiError ? error.message : '修改失败，请稍后重试'
  } finally {
    passwordBusy.value = false
  }
}

async function logout() {
  try {
    await api('/api/admin/logout', { method: 'POST' })
  } catch {
    // 会话已失效时直接回到登录页
  }
  markLoggedOut()
  await router.replace({ name: 'login' })
  toast('已退出登录', 'info')
}
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <div class="brand">
        <BrandMark :size="35" />
        <div class="brand-text">
          <div class="brand-name">XiaoyuRepeater</div>
          <div class="brand-sub">中继控制台</div>
        </div>
      </div>

      <nav class="nav" aria-label="主导航">
        <RouterLink
          v-for="item in navItems"
          :key="item.name"
          :to="{ name: item.name }"
          class="nav-item"
          active-class="active"
        >
          <Icon :name="item.icon" :size="18" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="sidebar-bottom">
        <div class="server-chip">
          <span class="dot online" />
          <span class="server-text">服务运行中</span>
          <span class="server-ver">v{{ authState.version }}</span>
        </div>
        <button
          v-if="!authState.envManaged"
          class="account-btn"
          type="button"
          title="修改密码"
          @click="openPassword"
        >
          <Icon name="lock" :size="16" />
          <span class="account-text">修改密码</span>
        </button>
        <button class="account-btn" type="button" title="退出登录" @click="logout">
          <Icon name="logout" :size="16" />
          <span class="account-text">退出登录</span>
        </button>
      </div>
    </aside>

    <main class="main">
      <RouterView v-slot="{ Component }">
        <Transition name="fade" mode="out-in">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </main>

    <Modal
      :open="passwordOpen"
      title="修改管理密码"
      width="sm"
      :closable="!passwordBusy"
      @close="closePassword"
    >
      <form id="password-form" class="password-form" @submit.prevent="submitPassword">
        <div class="field">
          <label class="field-label" for="current-password">当前密码</label>
          <input
            id="current-password"
            v-model="form.current"
            class="input"
            type="password"
            autocomplete="current-password"
          />
        </div>
        <div class="field">
          <label class="field-label" for="new-password">新密码</label>
          <input
            id="new-password"
            v-model="form.next"
            class="input"
            type="password"
            autocomplete="new-password"
            placeholder="至少 8 个字符"
          />
        </div>
        <div class="field">
          <label class="field-label" for="confirm-password">确认新密码</label>
          <input
            id="confirm-password"
            v-model="form.confirm"
            class="input"
            type="password"
            autocomplete="new-password"
          />
        </div>
        <p v-if="passwordError" class="error-text">{{ passwordError }}</p>
      </form>
      <template #footer>
        <button class="btn btn-ghost" type="button" :disabled="passwordBusy" @click="closePassword">取消</button>
        <button class="btn btn-primary" type="submit" form="password-form" :disabled="passwordBusy">
          <span v-if="passwordBusy" class="spinner" />
          保存
        </button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.shell {
  min-height: 100%;
}

.sidebar {
  position: fixed;
  top: 0;
  bottom: 0;
  left: 0;
  z-index: 40;
  display: flex;
  flex-direction: column;
  width: 228px;
  padding: 20px 14px 16px;
  background: var(--sidebar);
  border-right: 1px solid var(--border);
}

.brand {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 2px 8px 22px;
}

.brand-text {
  min-width: 0;
}

.brand-name {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.brand-sub {
  margin-top: 1px;
  font-size: 11px;
  color: var(--text-3);
}

.nav {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.nav-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  height: 38px;
  padding: 0 10px;
  border-radius: 8px;
  color: var(--text-2);
  font-size: 13.5px;
  transition: background 0.15s ease, color 0.15s ease;
}

.nav-item:hover {
  background: #1d1d1d;
  color: var(--text);
}

.nav-item.active {
  background: var(--surface-2);
  color: var(--text);
}

.nav-item.active :deep(.ri-icon) {
  color: var(--accent);
}

.sidebar-bottom {
  margin-top: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-top: 14px;
  border-top: 1px solid var(--border);
}

.server-chip {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 4px 10px 10px;
  font-size: 11.5px;
  color: var(--text-3);
}

.server-ver {
  margin-left: auto;
  font-family: var(--mono);
  font-size: 10.5px;
}

.account-btn {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 34px;
  padding: 0 10px;
  border-radius: 8px;
  color: var(--text-3);
  font-size: 12.5px;
  transition: background 0.15s ease, color 0.15s ease;
}

.account-btn:hover {
  background: #1d1d1d;
  color: var(--text);
}

.main {
  min-height: 100vh;
  margin-left: 228px;
  padding: 36px 38px 56px;
}

.password-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding-bottom: 10px;
}

@media (max-width: 900px) {
  .sidebar {
    position: sticky;
    top: 0;
    bottom: auto;
    width: 100%;
    height: auto;
    flex-direction: row;
    align-items: center;
    gap: 14px;
    padding: 10px 16px;
    border-right: 0;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
  }

  .brand {
    padding: 0;
    flex-shrink: 0;
  }

  .brand-sub {
    display: none;
  }

  .nav {
    flex-direction: row;
    gap: 2px;
  }

  .nav-item {
    height: 34px;
    padding: 0 10px;
    white-space: nowrap;
  }

  .sidebar-bottom {
    margin-top: 0;
    margin-left: auto;
    flex-direction: row;
    align-items: center;
    gap: 2px;
    padding-top: 0;
    border-top: 0;
    flex-shrink: 0;
  }

  .server-chip {
    display: none;
  }

  .account-btn {
    width: 34px;
    padding: 0;
    justify-content: center;
  }

  .account-text {
    display: none;
  }

  .main {
    margin-left: 0;
    padding: 26px 22px 44px;
  }
}

@media (max-width: 560px) {
  .brand-text {
    display: none;
  }

  .main {
    padding: 22px 16px 40px;
  }
}
</style>
