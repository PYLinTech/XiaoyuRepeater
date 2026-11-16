import { createRouter, createWebHistory } from 'vue-router'
import { authState, ensureSession } from './auth'
import AppShell from './components/AppShell.vue'
import KeysView from './views/KeysView.vue'
import LoginView from './views/LoginView.vue'
import OverviewView from './views/OverviewView.vue'
import RoomsView from './views/RoomsView.vue'
import SdkView from './views/SdkView.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
    {
      path: '/',
      component: AppShell,
      children: [
        { path: '', redirect: '/overview' },
        { path: 'overview', name: 'overview', component: OverviewView },
        { path: 'rooms', name: 'rooms', component: RoomsView },
        { path: 'keys', name: 'keys', component: KeysView },
        { path: 'sdk', name: 'sdk', component: SdkView },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/overview' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to) => {
  try {
    await ensureSession()
  } catch {
    // Session check failure is surfaced by the login page itself.
  }

  if (to.meta.public) {
    if (to.name === 'login' && authState.authenticated) {
      return { path: '/overview' }
    }
    return true
  }

  if (!authState.authenticated) {
    return {
      path: '/login',
      query: to.fullPath !== '/' && to.fullPath !== '/overview' ? { next: to.fullPath } : undefined,
    }
  }
  return true
})

export default router
