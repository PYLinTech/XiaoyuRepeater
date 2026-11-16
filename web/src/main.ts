import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { setUnauthorizedHandler } from './api'
import { markLoggedOut } from './auth'
import './assets/remixicon/remixicon.css'
import './style.css'

setUnauthorizedHandler(() => {
  markLoggedOut()
  const current = router.currentRoute.value
  if (current.name !== 'login') {
    router.replace({ path: '/login', query: { next: current.fullPath } })
  }
})

createApp(App).use(router).mount('#app')
