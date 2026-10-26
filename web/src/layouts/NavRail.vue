<template>
  <nav class="nav-rail" :class="{ 'is-collapsed': collapsed }" :aria-label="t('shell.mainNavigation')">
    <router-link class="mesh-brand" to="/" aria-label="TunnelMesh" @click="emit('navigate')">
      <svg class="mesh-glyph" viewBox="0 0 28 12" width="28" height="12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
        <circle cx="3" cy="6" r="2.25" /><circle cx="14" cy="6" r="2.25" /><circle cx="25" cy="6" r="2.25" /><path d="M5.25 6h6.5M16.25 6h6.5" />
      </svg>
      <span v-if="!collapsed" class="mesh-name"><strong>TunnelMesh</strong><small>{{ t('shell.console') }}</small></span>
    </router-link>
    <el-menu class="rail-menu" :router="true" :default-active="activePath" :collapse="collapsed" :collapse-transition="false">
      <template v-for="group in groups" :key="group.key">
        <li class="rail-group" :class="{ 'is-rule': collapsed }" role="presentation">
          <span v-if="!collapsed">{{ t(group.labelKey) }}</span>
        </li>
        <el-menu-item v-for="item in group.items" :key="item.path" :index="item.path" @click="emit('navigate')">
          <TmIcon :name="item.icon" :size="18" />
          <template #title><span>{{ t(item.labelKey) }}</span></template>
        </el-menu-item>
      </template>
    </el-menu>
    <button v-if="collapsible" type="button" class="rail-toggle" data-test="nav-toggle" :aria-expanded="!collapsed" :aria-label="collapsed ? t('shell.expand') : t('shell.collapse')" :title="(collapsed ? t('shell.expand') : t('shell.collapse')) + ' ['" @click="emit('toggle')">
      <TmIcon :name="collapsed ? 'chevron-right' : 'chevron-left'" :size="16" />
      <span v-if="!collapsed">{{ t('shell.collapse') }}</span>
    </button>
  </nav>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ElMenu, ElMenuItem } from 'element-plus'
import 'element-plus/es/components/menu/style/css'
import 'element-plus/es/components/menu-item/style/css'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import TmIcon from '../components/icons/TmIcon.vue'
import { useAuthStore } from '../stores/auth'
import { flatNavigation, navigationGroups } from './navigation'

defineProps<{ collapsed?: boolean; collapsible?: boolean }>()
const emit = defineEmits<{ navigate: []; toggle: [] }>()

const { t } = useI18n()
const route = useRoute()
const auth = useAuthStore()
const groups = computed(() => navigationGroups(auth.isAdmin))

// /agents/abc belongs to the Agents entry: the longest nav path that prefixes
// the current one wins, and "/" only matches itself.
const activePath = computed(() => {
  const current = route.path
  const match = flatNavigation(auth.isAdmin)
    .map(item => item.path)
    .filter(path => (path === '/' ? current === '/' : current === path || current.startsWith(path + '/')))
    .sort((a, b) => b.length - a.length)[0]
  return match ?? ''
})
</script>

<style scoped>
.nav-rail { display: flex; flex-direction: column; height: 100%; min-height: 0; background: var(--tm-paper); border-right: 1px solid var(--tm-line); }
.mesh-brand { display: flex; align-items: center; gap: var(--tm-s-3); height: var(--tm-topbar-h); padding: 0 var(--tm-s-5); flex: none; color: var(--tm-brand); text-decoration: none; border-bottom: 1px solid var(--tm-line); }
.is-collapsed .mesh-brand { justify-content: center; padding: 0; }
.mesh-glyph { flex: none; }
.mesh-name { display: flex; flex-direction: column; line-height: 1.15; min-width: 0; }
.mesh-name strong { color: var(--tm-ink); font-size: var(--tm-fs-md); font-weight: 600; letter-spacing: -0.01em; }
.mesh-name small { color: var(--tm-ink-3); font-size: var(--tm-fs-xs); }
.rail-menu { flex: 1; min-height: 0; overflow-y: auto; padding: var(--tm-s-2) var(--tm-s-3); --el-menu-item-height: 36px; }
.rail-group { list-style: none; padding: var(--tm-s-5) var(--tm-s-3) var(--tm-s-2); color: var(--tm-ink-3); font-size: var(--tm-fs-xs); font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; }
.rail-group.is-rule { padding: var(--tm-s-2) var(--tm-s-3); }
.rail-group.is-rule::after { content: ''; display: block; height: 1px; background: var(--tm-line); }
.rail-menu :deep(.el-menu-item) { gap: var(--tm-s-3); height: 36px; padding: 0 var(--tm-s-3); margin-bottom: 1px; border-radius: var(--tm-r-sm); font-size: var(--tm-fs-sm); font-weight: 500; line-height: 36px; position: relative; }
.rail-menu :deep(.el-menu-item.is-active) { background: var(--tm-brand-tint); color: var(--tm-brand); }
.rail-menu :deep(.el-menu-item.is-active)::before { content: ''; position: absolute; left: -12px; top: 8px; bottom: 8px; width: 3px; border-radius: 0 2px 2px 0; background: var(--tm-brand); }
.rail-menu :deep(.el-menu--collapse) { width: auto; }
.rail-toggle { flex: none; display: flex; align-items: center; gap: var(--tm-s-2); height: 40px; padding: 0 var(--tm-s-5); border: 0; border-top: 1px solid var(--tm-line); background: transparent; color: var(--tm-ink-2); font: inherit; font-size: var(--tm-fs-xs); cursor: pointer; }
.is-collapsed .rail-toggle { justify-content: center; padding: 0; }
.rail-toggle:hover { background: var(--tm-sunken); color: var(--tm-ink); }
</style>
