import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { initTheme } from './stores/theme'
import { clearSession } from './stores/session'
import { onUnauthorized } from './api/http'
import './styles/tokens.css'
import './styles/base.css'

onUnauthorized(() => {
  clearSession()
  const current = router.currentRoute.value
  if (current.name !== 'login') {
    void router.push({ name: 'login', query: { next: current.fullPath } })
  }
})

initTheme()

createApp(App).use(router).mount('#app')