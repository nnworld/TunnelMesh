<template>
  <section class="agent-detail">
    <el-page-header @back="$router.push('/agents')" content="Agent details" />
    <el-alert v-if="error" type="error" :title="error" show-icon />
    <el-skeleton v-if="loading" :rows="5" animated />
    <template v-else-if="metadata">
      <el-card class="metadata-card">
        <template #header><div class="card-header"><span>Metadata</span><el-tag v-if="metadata.stale" type="warning">Stale</el-tag><el-tag v-else type="success">Fresh</el-tag></div></template>
        <el-descriptions :column="3" border>
          <el-descriptions-item label="Agent ID">{{ metadata.agentId }}</el-descriptions-item>
          <el-descriptions-item label="Node ID">{{ metadata.nodeId }}</el-descriptions-item>
          <el-descriptions-item label="Epoch">{{ metadata.epoch }}</el-descriptions-item>
          <el-descriptions-item label="Revision">{{ metadata.revision }}</el-descriptions-item>
          <el-descriptions-item label="Reported at">{{ metadata.reportedAt }}</el-descriptions-item>
          <el-descriptions-item label="Updated at">{{ metadata.updatedAt }}</el-descriptions-item>
        </el-descriptions>
        <el-empty v-if="!metadata.items.length" description="No metadata reported" />
        <el-table v-else :data="metadata.items" class="metadata-table">
          <el-table-column prop="name" label="Name" />
          <el-table-column prop="source" label="Source" />
          <el-table-column label="Value"><template #default="scope"><el-tag v-if="scope.row.redacted" type="info">Redacted</el-tag><span v-else>{{ scope.row.value || '—' }}</span></template></el-table-column>
        </el-table>
      </el-card>
    </template>
    <el-empty v-else description="Metadata unavailable" />
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { getAgentMetadata, type AgentMetadata } from '../api/client'
const route = useRoute(); const loading = ref(true); const error = ref(''); const metadata = ref<AgentMetadata | null>(null)
onMounted(async () => { try { metadata.value = await getAgentMetadata(String(route.params.id), true) } catch (e) { error.value = e instanceof Error ? e.message : 'Failed to load metadata' } finally { loading.value = false } })
</script>
