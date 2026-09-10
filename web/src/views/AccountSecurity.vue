<template>
  <section class="tm-page">
    <PageHeader :title="t('security.title')" :description="t('security.description')" />
    <div class="tm-card form-card">
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item :label="t('security.currentPassword')"><el-input v-model="form.current" type="password" show-password autocomplete="current-password" /></el-form-item>
        <el-form-item :label="t('security.newPassword')"><el-input v-model="form.next" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-form-item :label="t('security.confirmPassword')"><el-input v-model="form.confirm" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-button type="primary" :loading="saving" native-type="submit">{{ t('security.save') }}</el-button>
      </el-form>
    </div>
  </section>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import { changePassword } from '../api/client'
import { accountErrorMessage } from '../i18n/errors'

const { t } = useI18n()
const saving = ref(false)
const form = reactive({ current: '', next: '', confirm: '' })

async function submit() {
  if (form.next.length < 12 || form.next.length > 128) { ElMessage.warning(t('security.length')); return }
  if (form.next !== form.confirm) { ElMessage.warning(t('security.mismatch')); return }
  saving.value = true
  try { await changePassword(form.current, form.next); form.current = form.next = form.confirm = ''; ElMessage.success(t('security.saved')) }
  catch (error) { ElMessage.error(accountErrorMessage(error)) }
  finally { saving.value = false }
}
</script>

<style scoped>.form-card { max-width: 620px; padding: 24px; }</style>
