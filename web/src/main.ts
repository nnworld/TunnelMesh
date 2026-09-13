import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { i18n } from './i18n'
// Element Plus styles are otherwise imported on demand from templates only,
// so programmatic Message/MessageBox components would render unstyled.
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'
import './styles/tokens.css'

createApp(App).use(createPinia()).use(router).use(i18n).mount('#app')
