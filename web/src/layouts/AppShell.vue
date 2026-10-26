<template>
  <div class="app-shell" :class="{ 'is-collapsed': preferences.navigationCollapsed }">
    <aside class="rail"><NavRail :collapsed="preferences.navigationCollapsed" collapsible @toggle="preferences.toggleNavigation()" /></aside>
    <TopBar class="bar" @menu="mobileOpen = true" @search="paletteOpen = true" />
    <el-drawer v-model="mobileOpen" class="mobile-drawer" direction="ltr" size="264px" :with-header="false">
      <NavRail @navigate="mobileOpen = false" />
    </el-drawer>
    <main class="content"><router-view /></main>
    <CommandPalette v-model="paletteOpen" />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ElDrawer } from 'element-plus'
import 'element-plus/es/components/drawer/style/css'
import { usePreferencesStore } from '../stores/preferences'
import CommandPalette from '../components/CommandPalette.vue'
import NavRail from './NavRail.vue'
import TopBar from './TopBar.vue'

const preferences = usePreferencesStore()
const mobileOpen = ref(false)
const paletteOpen = ref(false)

// Typing "[" into a field must never collapse the navigation.
function isTyping(target: EventTarget | null) {
  const el = target as HTMLElement | null
  return !!el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))
}

function onKeydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    paletteOpen.value = !paletteOpen.value
  } else if (event.key === '[' && !event.metaKey && !event.ctrlKey && !event.altKey && !isTyping(event.target)) {
    preferences.toggleNavigation()
  }
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<style scoped>
.app-shell {
  min-height: 100vh;
  display: grid;
  grid-template-columns: var(--tm-rail-w) minmax(0, 1fr);
  grid-template-rows: var(--tm-topbar-h) minmax(0, 1fr);
  grid-template-areas: "rail bar" "rail main";
  transition: grid-template-columns var(--tm-dur) var(--tm-ease);
}
.app-shell.is-collapsed { grid-template-columns: var(--tm-rail-w-collapsed) minmax(0, 1fr); }
.rail { grid-area: rail; position: sticky; top: 0; height: 100vh; z-index: 20; }
.bar { grid-area: bar; position: sticky; top: 0; z-index: 10; }
.content { grid-area: main; min-width: 0; padding: var(--tm-s-6) var(--tm-s-8) var(--tm-s-12); }
@media (prefers-reduced-motion: reduce) { .app-shell { transition: none; } }
@media (max-width: 760px) {
  .app-shell, .app-shell.is-collapsed { display: block; }
  .rail { display: none; }
  .content { padding: var(--tm-s-4) var(--tm-s-4) var(--tm-s-10); }
}
</style>

<style>
.mobile-drawer .el-drawer__body { padding: 0; }
</style>
