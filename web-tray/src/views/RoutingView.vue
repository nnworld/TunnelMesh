<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CheckResult, TunnelView } from '../api/types'
import { formatTimeOfDay } from '../format'
import { describe, useSettingsStore } from '../stores/settings'
import { useRoutingStore } from '../stores/routing'
import { technicalInput } from '../textInput'

const { t, te } = useI18n()
const routing = useRoutingStore()
const settings = useSettingsStore()

const notice = ref('')
const failure = ref('')
const agentsNotice = ref('')

const protocols = computed(() => routing.protocols)

const tokenPlaceholder = computed(() =>
  routing.tokenPresent ? t('routing.token.placeholderStored') : t('routing.token.placeholderEmpty'),
)

const agentOptions = computed(() =>
  routing.agents.map((agent) => ({
    value: agent.id,
    label: agent.name ? `${agent.name} (${agent.id})` : agent.id,
    online: agent.online,
    name: agent.name || agent.id,
  })),
)

function needsTarget(tunnel: TunnelView): boolean {
  // Only the raw forwarders have a fixed internal target; socks5 and http-proxy resolve
  // the destination per request, so asking for one would be wrong.
  return tunnel.protocol === 'tcp' || tunnel.protocol === 'udp' || tunnel.protocol === 'http'
}

function isProxy(tunnel: TunnelView): boolean {
  return tunnel.protocol === 'socks5' || tunnel.protocol === 'http-proxy'
}

function authModes(tunnel: TunnelView) {
  // The values config.Validate accepts, and nothing else: offering a fourth one would let
  // the operator build a configuration the tray's own 检测 then rejects. Remote validation is
  // not an auth mode, it is auth_url, and it composes with any of these.
  if (tunnel.protocol === 'socks5') return ['none', 'password']
  if (tunnel.protocol === 'http-proxy') return ['none', 'basic']
  return ['none']
}

// Check identifiers are dotted ("serverUrl.format"), which collides with vue-i18n's own
// path separator. Splitting them keeps the message tree navigable and lets an unknown
// identifier fall back to the raw id instead of rendering nothing.
function checkKey(check: CheckResult, field: 'label' | 'passed'): string | null {
  const [group, name] = check.id.split('.')
  if (!group || !name) return null
  const key = `checks.${group}.${name}.${field}`
  return te(key) ? key : null
}

function checkLabel(check: CheckResult): string {
  const key = checkKey(check, 'label')
  return key ? t(key) : check.id
}

function checkDetail(check: CheckResult): string {
  if (check.message) return check.message
  const key = checkKey(check, 'passed')
  return key ? t(key) : ''
}

function checkTagType(status: CheckResult['status']) {
  switch (status) {
    case 'passed':
      return 'success'
    case 'failed':
      return 'danger'
    case 'warning':
      return 'warning'
    default:
      return 'info'
  }
}

async function loadAgents() {
  agentsNotice.value = ''
  try {
    await routing.loadAgents()
    if (!routing.agents.length) agentsNotice.value = t('routing.tunnels.agent.empty')
  } catch (cause) {
    failure.value = t('routing.tunnels.agent.failed', { error: describe(cause) })
  }
}

async function validate() {
  notice.value = ''
  failure.value = ''
  try {
    await routing.validate()
    if (routing.report?.agents?.length) agentsNotice.value = ''
  } catch (cause) {
    failure.value = t('routing.save.failed', { error: describe(cause) })
  }
}

async function save() {
  notice.value = ''
  failure.value = ''
  try {
    const result = await routing.save()
    notice.value = result.runtimeError
      ? t('routing.save.restartFailed', { error: result.runtimeError })
      : result.restarted
        ? t('routing.save.restarted')
        : t('routing.save.saved')
    // The mode is shared with the general tab, so keep its draft in step with the file.
    settings.draft.mode = routing.mode
  } catch (cause) {
    failure.value = t('routing.save.failed', { error: describe(cause) })
  }
}

function addTunnel() {
  routing.addTunnel()
}

function removeTunnel(index: number) {
  routing.removeTunnel(index)
}

onMounted(() => {
  // Fetching the picker up front saves a click, but only when a server is configured:
  // without one the request would fail and leave a red banner on an empty form.
  if (routing.serverUrl && routing.tokenPresent) void loadAgents()
})
</script>

<template>
  <section class="tm-view" data-test="routing-view">
    <header class="tm-view-head">
      <div>
        <h2>{{ t('routing.title') }}</h2>
        <p>{{ t('routing.description') }}</p>
      </div>
      <div class="tm-tools">
        <el-button size="small" data-test="load-agents" :loading="routing.agentsLoading" @click="loadAgents">
          {{ t('routing.tunnels.agent.load') }}
        </el-button>
        <el-button size="small" data-test="validate" :loading="routing.validating" @click="validate">
          {{ routing.validating ? t('routing.validate.running') : t('routing.validate.button') }}
        </el-button>
        <el-button size="small" type="primary" data-test="save-routing" :loading="routing.saving" @click="save">
          {{ t('routing.save.button') }}
        </el-button>
      </div>
    </header>

    <el-form label-position="top" class="tm-form">
      <el-form-item :label="t('routing.serverUrl.label')">
        <el-input
          v-model="routing.serverUrl"
          :placeholder="t('routing.serverUrl.placeholder')"
          data-test="server-url"
          v-bind="technicalInput"
        />
        <p class="tm-hint">{{ t('routing.serverUrl.hint') }}</p>
      </el-form-item>

      <el-form-item :label="t('routing.token.label')">
        <div class="tm-token">
          <el-input
            v-model="routing.tokenEdit"
            type="password"
            show-password
            :placeholder="tokenPlaceholder"
            data-test="token"
            v-bind="technicalInput"
            @input="routing.tokenTouched = true"
          />
          <el-tag :type="routing.tokenPresent ? 'success' : 'info'" size="small" effect="plain" data-test="token-state">
            {{ routing.tokenPresent ? t('routing.token.present') : t('routing.token.absent') }}
          </el-tag>
          <el-checkbox v-model="routing.clearToken" data-test="clear-token">
            {{ t('routing.token.clear') }}
          </el-checkbox>
        </div>
        <p class="tm-hint">{{ t('routing.token.hint') }}</p>
      </el-form-item>

      <el-form-item :label="t('routing.tunnels.label')">
        <p v-if="!routing.tunnels.length" class="tm-hint" data-test="tunnels-empty">
          {{ t('routing.tunnels.empty') }}
        </p>

        <div v-for="(tunnel, index) in routing.tunnels" :key="index" class="tm-tunnels">
          <!-- The divider between entries is what the requirement asks for: several
               tunnels read as one list rather than a wall of inputs. -->
          <el-divider v-if="index > 0" data-test="tunnel-divider" />
          <el-card shadow="never" class="tm-tunnel" :data-test="`tunnel-${index}`">
            <template #header>
              <div class="tm-tunnel-head">
                <span>{{ t('routing.tunnels.entry', { index: index + 1 }) }}</span>
                <el-button size="small" text type="danger" :data-test="`remove-${index}`" @click="removeTunnel(index)">
                  {{ t('routing.tunnels.remove') }}
                </el-button>
              </div>
            </template>

            <div class="tm-grid">
              <el-form-item :label="t('routing.tunnels.protocol.label')">
                <el-select v-model="tunnel.protocol" :data-test="`protocol-${index}`" style="width: 100%">
                  <el-option
                    v-for="protocol in protocols"
                    :key="protocol"
                    :label="t(`routing.protocols.${protocol}`)"
                    :value="protocol"
                  />
                </el-select>
              </el-form-item>

              <el-form-item :label="t('routing.tunnels.name.label')">
                <el-input
                  v-model="tunnel.name"
                  :placeholder="t('routing.tunnels.name.placeholder')"
                  :data-test="`name-${index}`"
                  v-bind="technicalInput"
                />
              </el-form-item>

              <el-form-item :label="t('routing.tunnels.listen.label')">
                <el-input
                  v-model="tunnel.listen"
                  :placeholder="t('routing.tunnels.listen.placeholder')"
                  :data-test="`listen-${index}`"
                  v-bind="technicalInput"
                />
              </el-form-item>

              <el-form-item :label="t('routing.tunnels.agent.label')">
                <el-select
                  v-model="tunnel.agentId"
                  filterable
                  clearable
                  :placeholder="t('routing.tunnels.agent.placeholder')"
                  :loading="routing.agentsLoading"
                  :data-test="`agent-${index}`"
                  style="width: 100%"
                >
                  <el-option v-for="option in agentOptions" :key="option.value" :label="option.label" :value="option.value">
                    <span class="tm-agent">
                      <span>{{ option.name }}</span>
                      <el-tag :type="option.online ? 'success' : 'info'" size="small" effect="plain">
                        {{ option.online ? t('routing.tunnels.agent.online') : t('routing.tunnels.agent.offline') }}
                      </el-tag>
                    </span>
                  </el-option>
                  <template #empty>
                    <p class="tm-hint tm-select-empty">{{ t('routing.tunnels.agent.empty') }}</p>
                  </template>
                </el-select>
              </el-form-item>

              <template v-if="needsTarget(tunnel)">
                <el-form-item :label="t('routing.tunnels.targetHost.label')">
                  <el-input
                    v-model="tunnel.targetHost"
                    :placeholder="t('routing.tunnels.targetHost.placeholder')"
                    :data-test="`target-host-${index}`"
                    v-bind="technicalInput"
                  />
                </el-form-item>
                <el-form-item :label="t('routing.tunnels.targetPort.label')">
                  <el-input-number v-model="tunnel.targetPort" :min="0" :max="65535" controls-position="right" :data-test="`target-port-${index}`" />
                </el-form-item>
              </template>

              <template v-if="isProxy(tunnel)">
                <el-form-item :label="t('routing.tunnels.authMode.label')">
                  <el-select v-model="tunnel.authMode" :data-test="`auth-mode-${index}`" style="width: 100%">
                    <el-option
                      v-for="option in authModes(tunnel)"
                      :key="option"
                      :label="t(`routing.tunnels.authMode.${option}`)"
                      :value="option"
                    />
                  </el-select>
                </el-form-item>
                <el-form-item :label="t('routing.tunnels.authUrl.label')">
                  <el-input
                    v-model="tunnel.authUrl"
                    :placeholder="t('routing.tunnels.authUrl.placeholder')"
                    :data-test="`auth-url-${index}`"
                    v-bind="technicalInput"
                  />
                  <p class="tm-hint">{{ t('routing.tunnels.authUrl.hint') }}</p>
                </el-form-item>
              </template>

              <el-form-item>
                <div class="tm-switch">
                  <el-switch v-model="tunnel.allowRemote" :data-test="`allow-remote-${index}`" />
                  <span>{{ t('routing.tunnels.allowRemote.label') }}</span>
                </div>
                <p class="tm-hint">{{ t('routing.tunnels.allowRemote.hint') }}</p>
              </el-form-item>
            </div>

            <p v-if="isProxy(tunnel)" class="tm-hint">{{ t('routing.tunnels.proxyCredentials') }}</p>
          </el-card>
        </div>

        <el-button size="small" data-test="add-tunnel" @click="addTunnel">
          {{ t('routing.tunnels.add') }}
        </el-button>
        <p class="tm-hint">{{ t('routing.tunnels.agent.hint') }}</p>
      </el-form-item>
    </el-form>

    <el-card shadow="never" class="tm-card" data-test="validation-card">
      <template #header>
        <div class="tm-tunnel-head">
          <span>{{ t('routing.validate.heading') }}</span>
          <span v-if="routing.report" class="tm-hint">
            {{ t('routing.validate.checkedAt', { time: formatTimeOfDay(routing.report.checkedAt) }) }}
          </span>
        </div>
      </template>

      <p v-if="!routing.report" class="tm-hint" data-test="validation-empty">{{ t('routing.validate.empty') }}</p>
      <template v-else>
        <el-alert
          :type="routing.report.valid ? 'success' : 'error'"
          :closable="false"
          :title="routing.report.valid ? t('routing.validate.valid') : t('routing.validate.invalid')"
          data-test="validation-summary"
        />
        <ul class="tm-checks" data-test="validation-results">
          <li v-for="(check, index) in routing.report.checks" :key="`${check.id}-${index}`" class="tm-check">
            <el-tag :type="checkTagType(check.status)" size="small" effect="plain">
              {{ t(`routing.validate.status.${check.status}`) }}
            </el-tag>
            <span class="tm-check-label">{{ checkLabel(check) }}</span>
            <span v-if="check.tunnel" class="tm-check-tunnel">{{ check.tunnel }}</span>
            <span class="tm-check-message">{{ checkDetail(check) }}</span>
          </li>
        </ul>
      </template>
    </el-card>

    <footer class="tm-actions">
      <el-alert v-if="notice" type="success" :closable="false" :title="notice" data-test="routing-notice" />
      <el-alert v-if="failure" type="error" :closable="false" :title="failure" data-test="routing-error" />
      <el-alert v-if="agentsNotice" type="info" :closable="false" :title="agentsNotice" data-test="agents-notice" />
      <span v-if="routing.configPath" class="tm-hint"><code>{{ routing.configPath }}</code></span>
    </footer>
  </section>
</template>

<style scoped>
.tm-view {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.tm-view-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.tm-view-head h2 {
  margin: 0;
  font-size: 16px;
}
.tm-view-head p {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.tm-tools {
  display: flex;
  gap: 8px;
  align-items: center;
}
.tm-token {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
  flex-wrap: wrap;
}
.tm-tunnels {
  width: 100%;
}
.tm-tunnel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
}
.tm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 0 16px;
}
.tm-grid :deep(.el-form-item) {
  margin-bottom: 14px;
}
.tm-agent {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.tm-switch {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tm-hint {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.tm-select-empty {
  padding: 8px 12px;
}
.tm-checks {
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.tm-check {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 12px;
  flex-wrap: wrap;
}
.tm-check-label {
  font-weight: 600;
}
.tm-check-tunnel {
  color: var(--el-color-primary);
}
.tm-check-message {
  color: var(--el-text-color-secondary);
  word-break: break-all;
}
.tm-card :deep(.el-card__header) {
  padding: 10px 14px;
}
.tm-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
</style>
