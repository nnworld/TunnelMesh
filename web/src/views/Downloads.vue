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
              <el-tag effect="plain" round>{{ release.assets.length }} {{ t('downloads.platform') }}</el-tag>
              <a :href="release.releaseUrl" target="_blank" rel="noopener noreferrer">{{ t('downloads.releaseLink') }}</a>
            </div>
          </div>

          <div class="assets">
            <article v-for="asset in release.assets" :key="asset.platform" class="asset">
              <div class="asset-head">
                <span>{{ asset.platform }}</span>
                <el-tag size="small" effect="plain">{{ asset.archive.endsWith('.zip') ? 'zip' : 'tar.gz' }}</el-tag>
              </div>
              <code class="archive">{{ asset.archive }}</code>
              <code class="checksum-command" :title="checksumCommand(asset)">{{ checksumCommand(asset) }}</code>
              <div class="asset-actions">
                <a :href="asset.url" class="download" target="_blank" rel="noopener noreferrer">{{ t('downloads.download') }}</a>
                <el-button link type="primary" @click="copyChecksum(asset)">{{ t('downloads.checksumCommand') }}</el-button>
              </div>
            </article>
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
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { getDownloads, type DownloadAsset, type DownloadInfo } from '../api/client'

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

function checksumCommand(asset: DownloadAsset) {
  if (!release.value) return ''
  return `curl -fsSL ${release.value.checksumUrl} | grep '${asset.archive}' | sha256sum -c -`
}

async function copyChecksum(asset: DownloadAsset) {
  try {
    await navigator.clipboard.writeText(checksumCommand(asset))
    ElMessage.success(t('downloads.copied'))
  } catch {
    ElMessage.error(t('downloads.copyFailed'))
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
.assets { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:14px; }
.asset { display:flex; flex-direction:column; gap:12px; padding:16px; border:1px solid var(--tm-border); border-radius:10px; background:#fff; transition:border-color .2s ease, transform .2s ease; }
.asset:hover { border-color:#6aa9ff; transform:translateY(-1px); }
.asset-head { display:flex; align-items:center; justify-content:space-between; gap:10px; font-weight:700; }
.archive { padding:8px; color:#435a75; background:#f4f8fc; border-radius:6px; font-size:12px; overflow-wrap:anywhere; }
.checksum-command { display:block; padding:7px 8px; color:var(--tm-muted); background:#f8fafc; border-radius:6px; font-size:11px; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.asset-actions { display:flex; align-items:center; justify-content:space-between; gap:10px; margin-top:auto; }
.download { font-weight:650; }
.manifest { display:grid; grid-template-columns:repeat(auto-fit,minmax(260px,1fr)); gap:10px; }
.manifest div { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:12px 14px; background:#f7fbff; border:1px solid var(--tm-border); border-radius:8px; }
.manifest span { color:var(--tm-muted); font-size:13px; }
.upgrade-note { margin-top:2px; }
@media (prefers-reduced-motion: reduce) { .asset { transition:none; } .asset:hover { transform:none; } }
@media (max-width:760px) { .release-summary { grid-template-columns:1fr; } .release-summary dl { grid-template-columns:1fr; } .release-links { justify-content:flex-start; } }
</style>
