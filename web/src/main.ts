import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { i18n } from './i18n'
import { usePreferencesStore } from './stores/preferences'
// Element Plus styles are otherwise imported on demand from templates only,
// so programmatic Message/MessageBox components would render unstyled.
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'
import './styles/index.css'

const pinia = createPinia()
const app = createApp(App).use(pinia).use(router).use(i18n)
// The inline script in index.html has already set the theme before first paint; this
// takes over so the choice can change at run time and the system setting is followed.
usePreferencesStore(pinia).initTheme()
app.mount('#app')
