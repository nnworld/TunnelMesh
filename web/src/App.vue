<template><el-config-provider :locale="elementLocale"><AppShell v-if="auth.user"/><router-view v-else/></el-config-provider></template>
<script setup lang="ts">
import { computed } from 'vue'; import zhCn from 'element-plus/es/locale/lang/zh-cn'; import en from 'element-plus/es/locale/lang/en';
import { useAuthStore } from './stores/auth'; import { usePreferencesStore } from './stores/preferences'; import AppShell from './layouts/AppShell.vue';
const auth=useAuthStore(); const preferences=usePreferencesStore(); const elementLocale=computed(()=>preferences.locale==='zh-CN'?zhCn:en)
</script>
