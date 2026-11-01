<template>
  <el-dialog
    :model-value="visible"
    class="script-run-dialog"
    width="min(860px, 94vw)"
    :title="t('scriptRun.title', { agent: agent.name || agent.id })"
    @close="closeDialog"
  >
    <el-alert type="warning" :closable="false" :title="t('scriptRun.warning')" />
    <div class="script-form">
      <label class="script-label" for="script-run-input">{{ t('scriptRun.scriptLabel') }}</label>
      <el-input
        id="script-run-input"
        v-model="script"
        type="textarea"
        class="tm-mono script-input"
        :rows="8"
        resize="vertical"
        spellcheck="false"
        :placeholder="t('scriptRun.scriptPlaceholder')"
        :disabled="running"
      />
      <div class="script-actions">
        <el-select v-model="timeoutSec" class="script-timeout" :disabled="running" :aria-label="t('scriptRun.timeoutLabel')">
          <el-option v-for="option in timeoutOptions" :key="option" :label="`${option}s`" :value="option" />
        </el-select>
        <el-button v-if="!running" type="primary" :disabled="!script.trim()" data-test="script-run-submit" @click="run">{{ t('scriptRun.run') }}</el-button>
        <el-button v-else data-test="script-run-stop" @click="stop">{{ t('scriptRun.stop') }}</el-button>
      </div>
    </div>
    <div class="script-output-head">
      <span class="script-status" data-test="script-status" :data-state="status">{{ statusText }}</span>
    </div>
    <div class="script-output tm-mono" data-test="script-output">
      <pre v-if="outStdout || outStderr" class="script-stream"><span class="stream-stdout">{{ outStdout }}</span><span class="stream-stderr">{{ outStderr }}</span></pre>
      <p v-else class="script-empty">{{ t('scriptRun.noOutput') }}</p>
    </div>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { createAgentExecSession, openAgentExecSocket, type AgentExecFrame } from '../api/agentExec'
import { createOutputDecoder } from '../utils/scriptRun'

type RunState = 'idle' | 'running' | 'exited' | 'timeout' | 'error' | 'closed'

const props = defineProps<{ agent: { id: string; name?: string } }>()
const emit = defineEmits<{ closed: [] }>()
const { t } = useI18n()

const visible = ref(true)
const script = ref('')
const timeoutSec = ref(60)
const timeoutOptions = [30, 60, 300, 600]
const running = ref(false)
const status = ref<RunState>('idle')
const outStdout = ref('')
const outStderr = ref('')
const exitCode = ref<number | null>(null)
const truncated = ref(false)
const errorMessage = ref('')
let socket: WebSocket | null = null
const decodeStdout = createOutputDecoder()
const decodeStderr = createOutputDecoder()

const statusText = computed(() => {
  switch (status.value) {
    case 'running': return t('scriptRun.running')
    case 'exited': return t('scriptRun.done', { code: exitCode.value ?? 0 }) + (truncated.value ? ` · ${t('scriptRun.truncated')}` : '')
    case 'timeout': return t('scriptRun.timedOut')
    case 'error': return t('scriptRun.failed', { message: errorMessage.value })
    case 'closed': return t('scriptRun.connectionClosed')
    default: return ''
  }
})

function resetOutput() {
  outStdout.value = ''
  outStderr.value = ''
  exitCode.value = null
  truncated.value = false
  errorMessage.value = ''
}

async function run() {
  if (running.value || !script.value.trim()) return
  resetOutput()
  running.value = true
  status.value = 'running'
  try {
    const ticket = await createAgentExecSession(props.agent.id, globalThis.crypto.randomUUID())
    socket = openAgentExecSocket(ticket, script.value, timeoutSec.value, onFrame, onSocketClosed)
  } catch (error) {
    running.value = false
    status.value = 'error'
    errorMessage.value = error instanceof Error ? error.message : String(error)
  }
}

function onFrame(frame: AgentExecFrame) {
  switch (frame.type) {
    case 'stdout':
      outStdout.value += decodeStdout(frame.data)
      break
    case 'stderr':
      outStderr.value += decodeStderr(frame.data)
      break
    case 'exit':
      exitCode.value = frame.exitCode
      truncated.value = frame.truncated
      status.value = frame.timedOut ? 'timeout' : 'exited'
      running.value = false
      break
    case 'error':
      errorMessage.value = frame.message
      status.value = 'error'
      running.value = false
      break
  }
}

function onSocketClosed() {
  socket = null
  if (running.value) {
    running.value = false
    if (status.value === 'running') status.value = 'closed'
  }
}

// Closing the socket ends the run on the server, which tells the agent to kill the
// script's process group. Stopping is therefore the same action as closing.
function stop() {
  socket?.close()
}

function closeDialog() {
  stop()
  visible.value = false
  emit('closed')
}

onBeforeUnmount(() => stop())
</script>

<style scoped>
.script-form { display: grid; grid-template-columns: minmax(0, 1fr); gap: var(--tm-s-2); margin-top: var(--tm-s-4); }
.script-label { color: var(--tm-ink-2); font-size: var(--tm-fs-sm); font-weight: 500; }
.script-input :deep(.el-textarea__inner) { background: var(--tm-terminal-bg); color: var(--tm-ink); font-family: var(--tm-font-mono); font-size: var(--tm-fs-sm); }
.script-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: var(--tm-s-2); }
.script-timeout { width: 120px; }
.script-output-head { display: flex; align-items: center; justify-content: space-between; margin-top: var(--tm-s-4); }
.script-status { color: var(--tm-ink-2); font-size: var(--tm-fs-sm); }
.script-output { margin-top: var(--tm-s-2); min-height: 160px; max-height: 360px; overflow: auto; padding: var(--tm-s-3) var(--tm-s-4); background: var(--tm-terminal-bg); border-radius: var(--tm-r-sm); }
.script-stream { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--tm-ink); font-size: var(--tm-fs-sm); line-height: var(--tm-lh-md); }
.stream-stderr { color: var(--tm-danger-ink); }
.script-empty { margin: 0; color: var(--tm-ink-3); font-size: var(--tm-fs-sm); }
</style>
