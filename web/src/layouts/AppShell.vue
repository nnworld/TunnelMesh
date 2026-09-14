<template>
  <div class="app-shell">
    <header class="topbar">
      <button class="nav-trigger" :aria-label="t('shell.expand')" @click="mobileOpen = true">☰</button>
      <router-link class="mesh-brand" to="/" aria-label="TunnelMesh">
        <span class="mesh-mark"><i/><i/><i/></span><strong>TunnelMesh</strong><small>{{ t('shell.console') }}</small>
      </router-link>
      <div class="topbar-actions">
        <el-select :model-value="preferences.locale" class="locale-select" :aria-label="t('common.language')" @change="changeLocale">
          <el-option label="简体中文" value="zh-CN"/><el-option label="English" value="en-US"/>
        </el-select>
        <el-dropdown>
          <button class="user-button"><span class="avatar">{{ auth.user?.username.slice(0, 1).toUpperCase() }}</span>{{ auth.user?.username }}</button>
          <template #dropdown><el-dropdown-menu>
            <el-dropdown-item @click="router.push('/account/security')">{{ t('navigation.security') }}</el-dropdown-item>
            <el-dropdown-item divided @click="signOut">{{ t('common.logout') }}</el-dropdown-item>
          </el-dropdown-menu></template>
        </el-dropdown>
      </div>
    </header>
    <aside class="sidebar"><NavigationMenu/></aside>
    <el-drawer v-model="mobileOpen" class="mobile-drawer" direction="ltr" size="260px" :with-header="false"><NavigationMenu @click="mobileOpen=false"/></el-drawer>
    <main class="content"><router-view/></main>
  </div>
</template>

<script setup lang="ts">
import { defineComponent, h, ref } from 'vue'
import { ElMenu, ElMenuItem } from 'element-plus'
import 'element-plus/es/components/menu/style/css'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { usePreferencesStore } from '../stores/preferences'
import type { SupportedLocale } from '../i18n'

const auth = useAuthStore(); const preferences = usePreferencesStore(); const router = useRouter(); const { t } = useI18n(); const mobileOpen = ref(false)
const menuItems = () => [
  ['/', 'navigation.dashboard'], ['/agents', 'navigation.agents'], ['/clients', 'navigation.clients'], ['/routes', 'navigation.routes'], ['/tokens', 'navigation.tokens'], ['/remote-servers', 'navigation.remoteServers'], ['/credentials', 'navigation.credentials'],
  ...(auth.isAdmin ? [['/servers', 'navigation.servers'], ['/users', 'navigation.users'], ['/audit-logs', 'navigation.audits'], ['/downloads', 'navigation.downloads']] : []),
]
const NavigationMenu = defineComponent({ emits: ['click'], setup(_, { emit }) { return () => h(ElMenu, { router: true, defaultActive: router.currentRoute.value.path }, () => menuItems().map(([path, key]) => h(ElMenuItem, { index: path, onClick: () => emit('click') }, () => t(key)))) } })
function changeLocale(value: SupportedLocale) { preferences.setLocale(value) }
function signOut() { auth.logout(); router.push('/login') }
</script>

<style scoped>
.app-shell{min-height:100vh;display:grid;grid-template-columns:var(--tm-sidebar) 1fr;grid-template-rows:var(--tm-header) 1fr;grid-template-areas:"top top" "side main"}.topbar{grid-area:top;z-index:10;display:flex;align-items:center;justify-content:space-between;padding:0 20px;background:#fff;border-bottom:1px solid var(--tm-border)}.mesh-brand{display:flex;align-items:center;gap:10px;color:var(--tm-text);text-decoration:none}.mesh-brand small{color:var(--tm-muted);font-size:12px;font-weight:500}.mesh-mark{position:relative;display:flex;align-items:center;gap:4px;width:30px}.mesh-mark::before{content:"";position:absolute;left:4px;right:4px;height:2px;background:var(--tm-primary)}.mesh-mark i{z-index:1;width:8px;height:8px;border:2px solid var(--tm-primary);border-radius:50%;background:#fff}.topbar-actions{display:flex;align-items:center;gap:12px}.locale-select{width:118px}.user-button,.nav-trigger{border:0;background:transparent;cursor:pointer;color:var(--tm-text)}.user-button{display:flex;align-items:center;gap:8px}.avatar{display:grid;place-items:center;width:28px;height:28px;border-radius:6px;background:#e8f3ff;color:var(--tm-primary);font-weight:700}.sidebar{grid-area:side;background:#fff;border-right:1px solid var(--tm-border);padding-top:12px}.sidebar :deep(.el-menu){border-right:0}.content{grid-area:main;min-width:0;padding:20px 24px}.nav-trigger{display:none;font-size:20px}.mobile-drawer{display:none}@media(max-width:760px){.app-shell{display:block}.topbar{height:var(--tm-header);padding:0 12px}.nav-trigger{display:block}.sidebar{display:none}.mobile-drawer{display:block}.content{padding:16px}.mesh-brand small{display:none}.locale-select{width:105px}}
</style>
