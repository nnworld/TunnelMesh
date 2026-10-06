import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { i18n, applyLanguagePreference } from './i18n'
import { applyTheme } from './theme'
// Programmatic components are not resolved by the template compiler, so their styles
// have to be imported explicitly.
import 'element-plus/es/components/message/style/css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles/tokens.css'

// Apply the system appearance before the first paint: waiting for the settings request
// would flash the wrong theme for a moment on every launch.
applyLanguagePreference('system')
applyTheme('system')

createApp(App).use(createPinia()).use(i18n).mount('#app')
