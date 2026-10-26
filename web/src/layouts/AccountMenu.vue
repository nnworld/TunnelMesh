<template>
  <el-dropdown trigger="click" placement="bottom-end" :hide-on-click="true" @command="onCommand">
    <button type="button" class="account-button" data-test="account-menu" :aria-label="t('common.account')">
      <span class="avatar" aria-hidden="true">{{ initial }}</span>
      <span class="account-name">{{ auth.user?.username }}</span>
      <TmIcon name="chevron-down" :size="14" />
    </button>
    <template #dropdown>
      <el-dropdown-menu class="account-dropdown">
        <div class="account-group-label">{{ t('shell.theme') }}</div>
        <el-dropdown-item v-for="option in themes" :key="option.value" :command="`theme:${option.value}`">
          <TmIcon :name="option.icon" :size="16" /><span class="item-label">{{ t(option.labelKey) }}</span>
          <TmIcon v-if="preferences.theme === option.value" name="check" :size="16" class="item-check" />
        </el-dropdown-item>
        <div class="account-group-label">{{ t('common.language') }}</div>
        <el-dropdown-item v-for="option in locales" :key="option.value" :command="`locale:${option.value}`">
          <TmIcon name="globe" :size="16" /><span class="item-label">{{ option.label }}</span>
          <TmIcon v-if="preferences.locale === option.value" name="check" :size="16" class="item-check" />
        </el-dropdown-item>
        <el-dropdown-item divided command="security"><TmIcon name="shield" :size="16" /><span class="item-label">{{ t('navigation.security') }}</span></el-dropdown-item>
        <el-dropdown-item command="logout"><TmIcon name="logout" :size="16" /><span class="item-label">{{ t('common.logout') }}</span></el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ElDropdown, ElDropdownItem, ElDropdownMenu } from 'element-plus'
import 'element-plus/es/components/dropdown/style/css'
import 'element-plus/es/components/dropdown-menu/style/css'
import 'element-plus/es/components/dropdown-item/style/css'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import TmIcon from '../components/icons/TmIcon.vue'
import { useAuthStore } from '../stores/auth'
import { usePreferencesStore } from '../stores/preferences'
import type { SupportedLocale } from '../i18n'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const preferences = usePreferencesStore()

const initial = computed(() => auth.user?.username.slice(0, 1).toUpperCase() ?? '')
const themes = [
  { value: 'light', icon: 'sun', labelKey: 'shell.themeLight' },
  { value: 'dark', icon: 'moon', labelKey: 'shell.themeDark' },
  { value: 'system', icon: 'monitor', labelKey: 'shell.themeSystem' },
] as const
const locales = [{ value: 'zh-CN', label: '简体中文' }, { value: 'en-US', label: 'English' }] as const

function onCommand(command: string) {
  const [kind, value] = command.split(':')
  if (kind === 'theme') preferences.setTheme(value as 'light' | 'dark' | 'system')
  else if (kind === 'locale') preferences.setLocale(value as SupportedLocale)
  else if (kind === 'security') void router.push('/account/security')
  else if (kind === 'logout') { auth.logout(); void router.push('/login') }
}
</script>

<style scoped>
.account-button { display: inline-flex; align-items: center; gap: var(--tm-s-2); height: 34px; padding: 0 var(--tm-s-2) 0 var(--tm-s-1); border: 1px solid transparent; border-radius: var(--tm-r-sm); background: transparent; color: var(--tm-ink); font: inherit; font-size: var(--tm-fs-sm); cursor: pointer; }
.account-button:hover { background: var(--tm-sunken); }
.avatar { display: grid; place-items: center; width: 26px; height: 26px; border-radius: var(--tm-r-xs); background: var(--tm-brand-tint); color: var(--tm-brand); font-weight: 600; font-size: var(--tm-fs-xs); }
.account-name { max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 760px) { .account-name { display: none; } }
</style>

<style>
.account-dropdown { min-width: 200px; }
.account-dropdown .account-group-label { padding: var(--tm-s-2) var(--tm-s-4) var(--tm-s-1); color: var(--tm-ink-2); font-size: var(--tm-fs-xs); font-weight: 600; letter-spacing: 0.04em; }
.account-dropdown .el-dropdown-menu__item { gap: var(--tm-s-3); }
.account-dropdown .item-label { flex: 1; }
.account-dropdown .item-check { color: var(--tm-brand); }
</style>
