<template>
  <section class="system-inventory tm-card" data-test="system-inventory">
    <div class="inv-head">
      <h2>{{ t('inventory.title') }}</h2>
      <p class="inv-desc">{{ t('inventory.description') }}</p>
    </div>
    <p v-if="!inventory.hasAny" class="inv-empty">{{ t('inventory.empty') }}</p>
    <div v-else class="inv-grid">
      <section v-if="inventory.os" class="inv-block hue-brand">
        <h3>{{ t('inventory.os') }}</h3>
        <DescriptionList :items="[
          { label: t('inventory.os'), value: inventory.os.name },
          { label: t('inventory.kernel'), value: inventory.os.kernel },
          { label: t('inventory.arch'), value: inventory.os.arch, mono: true },
          { label: t('inventory.hostname'), value: inventory.os.hostname, mono: true },
        ]" />
      </section>
      <section v-if="inventory.cpu || inventory.gpus.length" class="inv-block hue-sky">
        <h3>{{ t('inventory.cpu') }}</h3>
        <DescriptionList :items="[
          { label: t('inventory.model'), value: inventory.cpu?.model },
          { label: t('inventory.logicalCpus'), value: inventory.cpu?.logicalCpus, mono: true },
          { label: t('inventory.gpu'), value: inventory.gpus.join(' · ') },
        ]" />
      </section>
      <section v-if="inventory.memory" class="inv-block hue-violet">
        <h3>{{ t('inventory.memory') }}</h3>
        <DescriptionList :items="[{ label: t('inventory.total'), value: formatBytes(inventory.memory.totalBytes), mono: true }]" />
      </section>
      <section v-if="inventory.disks.length" class="inv-block hue-warn inv-wide">
        <h3>{{ t('inventory.disks') }}</h3>
        <div class="inv-table-wrap">
          <table class="inv-table">
            <thead><tr><th>{{ t('inventory.mount') }}</th><th>{{ t('inventory.fs') }}</th><th>{{ t('inventory.capacity') }}</th></tr></thead>
            <tbody>
              <tr v-for="disk in inventory.disks" :key="disk.mount">
                <td class="tm-mono">{{ disk.mount }}</td>
                <td>{{ disk.fs }}</td>
                <td class="tm-num">{{ formatBytes(disk.totalBytes) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section v-if="inventory.nics.length" class="inv-block inv-wide hue-lime">
        <h3>{{ t('inventory.nics') }}</h3>
        <div class="inv-table-wrap">
          <table class="inv-table">
            <thead><tr><th>{{ t('inventory.interface') }}</th><th>{{ t('inventory.mac') }}</th><th>{{ t('inventory.addresses') }}</th></tr></thead>
            <tbody>
              <tr v-for="nic in inventory.nics" :key="nic.name">
                <td class="tm-mono">{{ nic.name }}</td>
                <td class="tm-mono">{{ nic.mac || '—' }}</td>
                <td class="tm-mono">{{ nic.addresses.join(', ') || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DescriptionList from './DescriptionList.vue'
import { formatBytes, parseInventory } from '../utils/inventory'

const props = defineProps<{ values: Record<string, string> }>()
const { t } = useI18n()
const inventory = computed(() => parseInventory(props.values))
</script>

<style scoped>
.system-inventory { padding: var(--tm-s-5); }
.inv-head h2 { margin: 0; font-size: var(--tm-fs-md); font-weight: 600; color: var(--tm-ink); }
.inv-desc { margin: var(--tm-s-1) 0 var(--tm-s-4); color: var(--tm-ink-2); font-size: var(--tm-fs-xs); }
.inv-empty { margin: 0; color: var(--tm-ink-3); font-size: var(--tm-fs-sm); }
.inv-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: var(--tm-s-4); }
.inv-block { position: relative; padding: var(--tm-s-4); border: 1px solid var(--tm-line); border-radius: var(--tm-r-sm); background: var(--tm-paper); min-width: 0; }
.inv-block::before { content: ''; position: absolute; left: 0; top: var(--tm-s-4); bottom: var(--tm-s-4); width: 2px; border-radius: 2px; background: var(--hue, var(--tm-brand)); }
.inv-block h3 { margin: 0 0 var(--tm-s-3); color: var(--tm-ink-2); font-size: var(--tm-fs-xs); font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; }
.inv-wide { grid-column: 1 / -1; }
.hue-brand { --hue: var(--tm-brand); }
.hue-sky { --hue: var(--tm-sky); }
.hue-violet { --hue: var(--tm-violet); }
.hue-warn { --hue: var(--tm-warn); }
.hue-lime { --hue: var(--tm-lime); }
.inv-table-wrap { overflow-x: auto; }
.inv-table { width: 100%; border-collapse: collapse; font-size: var(--tm-fs-sm); }
.inv-table th { padding: var(--tm-s-2) var(--tm-s-3); text-align: left; color: var(--tm-ink-2); font-size: var(--tm-fs-xs); font-weight: 600; background: var(--tm-sunken); }
.inv-table td { padding: var(--tm-s-2) var(--tm-s-3); border-top: 1px solid var(--tm-line); color: var(--tm-ink); overflow-wrap: anywhere; }
</style>
