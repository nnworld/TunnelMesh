<template>
  <section class="tm-page">
    <PageHeader :title="t('downloads.title')" :description="t('downloads.description')">
      <el-button :loading="loading" @click="load">{{ t('downloads.refresh') }}</el-button>
    </PageHeader>

    <div class="tm-card">
      <DataState
        :loading="loading"
        :error="error"
        :empty="!release"
        :error-label="t('downloads.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('downloads.empty')"
        :rows="6"
        @retry="load"
      >
        <div v-if="release" class="release">
          <div class="release-summary">
            <div>
              <p>{{ t('downloads.currentRelease') }}</p>
              <h2>{{ release.version }}</h2>
            </div>
            <dl>
              <div><dt>{{ t('downloads.commit') }}</dt><dd><code>{{ release.commit }}</code></dd></div>
              <div><dt>{{ t('downloads.buildTime') }}</dt><dd><code>{{ release.buildTime }}</code></dd></div>
              <div><dt>{{ t('downloads.schemaVersion') }}</dt><dd>{{ release.schemaVersion }}</dd></div>
              <div><dt>{{ t('downloads.repository') }}</dt><dd>{{ release.repository }}</dd></div>
            </dl>
            <div class="release-links">
              <a :href="GITHUB_RELEASES_URL" target="_blank" rel="noopener noreferrer">{{ t('downloads.releaseLink') }}</a>
            </div>
          </div>

          <div class="manifest">
            <div>
              <span>{{ t('downloads.checksum') }}</span>
              <a :href="release.checksumUrl" target="_blank" rel="noopener noreferrer">SHA256SUMS</a>
            </div>
            <div>
              <span>{{ t('downloads.manifest') }}</span>
              <a :href="release.manifestUrl" target="_blank" rel="noopener noreferrer">manifest.json</a>
            </div>
          </div>

          <el-alert class="upgrade-note" type="warning" show-icon :closable="false" :title="t('downloads.upgradeNote')" />
        </div>
      </DataState>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { getDownloads, type DownloadInfo } from '../api/client'

const GITHUB_RELEASES_URL = 'https://github.com/nnworld/TunnelMesh/releases'

const { t } = useI18n()
const release = ref<DownloadInfo | null>(null)
const loading = ref(false)
const error = ref(false)

async function load() {
  loading.value = true
  error.value = false
  try {
    release.value = await getDownloads()
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.release { display:grid; grid-template-columns:minmax(0,1fr); gap:22px; }
.release-summary { display:grid; grid-template-columns:minmax(190px,.75fr) 1.5fr; gap:20px; align-items:start; padding:18px; border:1px solid var(--tm-border); border-radius:12px; background:linear-gradient(135deg,#f7fbff 0%,#eef7ff 100%); }
.release-summary p { margin:0 0 5px; color:var(--tm-muted); font-size:12px; letter-spacing:.08em; text-transform:uppercase; }
.release-summary h2 { margin:0; font-size:30px; line-height:1.15; }
.release-summary dl { display:grid; grid-template-columns:repeat(2,minmax(160px,1fr)); gap:14px; margin:0; }
.release-summary dt { color:var(--tm-muted); font-size:12px; }
.release-summary dd { margin:3px 0 0; font-weight:650; overflow-wrap:anywhere; }
.release-summary code { font-size:12px; }
.release-links { display:flex; align-items:center; justify-content:flex-end; gap:12px; grid-column:1/-1; }
.release-links a { font-weight:650; }
.manifest { display:grid; grid-template-columns:repeat(auto-fit,minmax(260px,1fr)); gap:10px; }
.manifest div { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:12px 14px; background:#f7fbff; border:1px solid var(--tm-border); border-radius:8px; }
.manifest span { color:var(--tm-muted); font-size:13px; }
.upgrade-note { margin-top:2px; }
@media (max-width:760px) { .release-summary { grid-template-columns:1fr; } .release-summary dl { grid-template-columns:1fr; } .release-links { justify-content:flex-start; } }
</style>
