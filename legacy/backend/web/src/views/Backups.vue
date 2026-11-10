<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Backups &amp; Recovery</h1>
        <p class="muted">Crash-consistent local snapshot operations and backup metadata.</p>
      </div>
      <select v-model="projectId" style="width:auto" @change="load">
        <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>
    <template v-else>
      <div class="card notice">
        <strong>Snapshots stay on the assigned agent host.</strong>
        Capture and restore use crash-consistent snapshots only. Off-host backup, scheduling, and recovery after loss of that host are unavailable. A restore replaces the selected VM's current state.
      </div>

      <div class="card catalog">
        <div class="section-head">
          <div><h2>Backup records</h2><span class="muted">Metadata only · off-host copy unavailable</span></div>
          <span class="pill info">{{ backups.length }} records</span>
        </div>
        <table v-if="backups.length">
          <thead><tr><th>Record</th><th>Service / instance</th><th>State</th><th>Off-host</th><th>Artifact</th><th>Integrity</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="b in backups" :key="b.id">
              <td><span class="mono">{{ short(b.id) }}</span><div class="muted">{{ b.consistency || 'crash_consistent' }}</div></td>
              <td><div>{{ b.service_id || '—' }}</div><div class="mono muted">{{ b.instance_id || '—' }}</div></td>
              <td><span class="pill info">{{ b.state || 'metadata_only' }}</span></td>
              <td><span class="pill info">{{ b.off_host_status || 'unavailable' }}</span></td>
              <td><div>{{ b.size_bytes == null ? 'No backup artifact' : bytes(b.size_bytes) }}</div><div class="mono muted">{{ b.artifact_sha256 || 'No artifact digest' }}</div></td>
              <td><span class="pill info">{{ b.integrity_status || 'not_available' }}</span></td>
              <td class="muted">{{ ago(b.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">No backup metadata records for this project.</div>
      </div>

      <div class="card catalog">
        <div class="section-head">
          <div><h2>Project snapshots</h2><span class="muted">Capture status, operation links, and verified integrity metadata</span></div>
        </div>
        <table v-if="projectSnapshots.length">
          <thead><tr><th>Snapshot</th><th>Service / instance</th><th>Capture / restore</th><th>Artifact</th><th>Integrity</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="s in projectSnapshots" :key="s.id">
              <td class="mono">{{ short(s.id) }}</td>
              <td><div>{{ s.service_id || '—' }}</div><div class="mono muted">{{ s.instance_id || '—' }}</div></td>
              <td><span class="pill info">{{ s.capture_status || 'unavailable' }}</span><div class="muted">restore: {{ s.restore_status || 'not_requested' }}</div></td>
              <td>{{ s.size_bytes == null ? 'Not captured' : bytes(s.size_bytes) }}<div class="mono muted">{{ s.artifact_sha256 || 'No artifact digest' }}</div></td>
              <td><span class="pill info">{{ s.integrity_status || 'not_available' }}</span></td>
              <td class="muted">{{ ago(s.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">No snapshots for this project.</div>
      </div>

      <div class="card catalog">
        <div class="section-head">
          <div>
            <h2>Instance snapshots</h2>
            <span class="muted">Capture requires a running Instance. Restore requires a stopped or failed Instance and a verified snapshot on the same node.</span>
          </div>
          <select v-model="instanceId" style="width:auto" @change="loadInstanceSnapshots">
            <option value="">Select instance…</option>
            <option v-for="i in instances" :key="i.id" :value="i.id">{{ i.service_id || 'Unknown service' }} · {{ i.id }} ({{ i.state }})</option>
          </select>
        </div>

        <div v-if="actionMessage" class="notice action-message">{{ actionMessage }}</div>
        <div v-if="actionError" class="error-bar">{{ actionError }}</div>
        <div v-if="snapErr" class="error-bar">{{ snapErr }}</div>
        <div v-if="instanceId" class="action-row">
          <div class="muted">Selected Instance state: {{ selectedInstance?.state || 'unknown' }} · node {{ selectedInstance?.node_id || 'unassigned' }}</div>
          <button class="ghost" :disabled="busy || !canCapture" @click="captureSnapshot">
            {{ busy === 'capture' ? 'Queueing capture…' : 'Capture crash-consistent snapshot' }}
          </button>
        </div>

        <table v-if="instanceSnapshots.length">
          <thead><tr><th>Snapshot</th><th>Capture / restore</th><th>Artifact</th><th>Integrity</th><th>Created</th><th>Action</th></tr></thead>
          <tbody>
            <tr v-for="s in instanceSnapshots" :key="s.id">
              <td class="mono">{{ short(s.id) }}</td>
              <td><span class="pill info">{{ s.capture_status || 'unavailable' }}</span><div class="muted">restore: {{ s.restore_status || 'not_requested' }}</div></td>
              <td>{{ s.size_bytes == null ? 'Not captured' : bytes(s.size_bytes) }}<div class="mono muted">{{ s.artifact_sha256 || 'No artifact digest' }}</div></td>
              <td><span class="pill info">{{ s.integrity_status || 'not_available' }}</span></td>
              <td class="muted">{{ ago(s.created_at) }}</td>
              <td>
                <button class="ghost" :disabled="busy || !canRestore(s)" @click="restoreSnapshot(s)">
                  {{ busy === `restore:${s.id}` ? 'Queueing restore…' : 'Restore' }}
                </button>
                <div v-if="!canRestore(s)" class="muted action-hint">Restore needs a stopped/failed Instance and a verified snapshot on its assigned node.</div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="instanceId && !snapErr" class="empty">No snapshots recorded for this Instance.</div>
        <div v-else-if="!instanceId" class="empty">Select an Instance to view snapshots and available actions.</div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { ago, listAll, post } from '../lib/api.js'

const projects = ref([])
const projectId = ref('')
const backups = ref([])
const projectSnapshots = ref([])
const instances = ref([])
const instanceId = ref('')
const instanceSnapshots = ref([])
const error = ref('')
const snapErr = ref('')
const actionMessage = ref('')
const actionError = ref('')
const busy = ref('')

const selectedInstance = computed(() => instances.value.find((item) => item.id === instanceId.value) || null)
const canCapture = computed(() => selectedInstance.value?.state === 'running' && !!selectedInstance.value?.node_id)

const short = (value) => {
  const text = String(value || '—')
  return text.length > 24 ? `${text.slice(0, 20)}…` : text
}
const bytes = (value) => {
  if (value == null) return '—'
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(1)} GB`
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MB`
  return `${value} B`
}
const idempotencyKey = () => `ui-${globalThis.crypto?.randomUUID?.() || `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`}`
const canRestore = (snapshot) => ['stopped', 'failed'].includes(selectedInstance.value?.state) &&
  !!selectedInstance.value?.node_id && snapshot.node_affinity_id === selectedInstance.value.node_id &&
  snapshot.capture_status === 'captured' && snapshot.integrity_status === 'verified' && !!snapshot.artifact_sha256 && snapshot.size_bytes > 0 &&
  !['queued', 'running'].includes(snapshot.restore_status)

async function load() {
  if (!projectId.value) return
  error.value = ''
  snapErr.value = ''
  actionMessage.value = ''
  actionError.value = ''
  instanceSnapshots.value = []
  try {
    const pid = encodeURIComponent(projectId.value)
    const [backupRows, snapshotRows, instanceRows] = await Promise.all([
      listAll(`/projects/${pid}/backups`),
      listAll(`/projects/${pid}/snapshots`),
      listAll(`/projects/${pid}/instances`),
    ])
    backups.value = backupRows
    projectSnapshots.value = snapshotRows
    instances.value = instanceRows
    if (instanceId.value && !instanceRows.some((item) => item.id === instanceId.value)) instanceId.value = ''
    if (instanceId.value) await loadInstanceSnapshots()
  } catch (err) {
    error.value = err.message || 'Failed to load PostgreSQL backup metadata'
  }
}

async function loadInstanceSnapshots() {
  instanceSnapshots.value = []
  snapErr.value = ''
  actionMessage.value = ''
  actionError.value = ''
  if (!instanceId.value) return
  try {
    instanceSnapshots.value = await listAll(`/instances/${encodeURIComponent(instanceId.value)}/snapshots`)
  } catch (err) {
    snapErr.value = err.message || 'Failed to load Instance snapshots'
  }
}

async function captureSnapshot() {
  if (!canCapture.value || busy.value) return
  busy.value = 'capture'
  actionMessage.value = ''
  actionError.value = ''
  try {
    const result = await post(`/instances/${encodeURIComponent(instanceId.value)}/snapshots/capture`, {
      consistency: 'crash_consistent',
      idempotency_key: idempotencyKey(),
    })
    await load()
    actionMessage.value = `Snapshot capture queued as operation ${result.operation.id}. The artifact remains on node ${result.snapshot.node_affinity_id || 'assigned agent'}.`
  } catch (err) {
    actionError.value = err.message || 'Could not queue snapshot capture'
  } finally {
    busy.value = ''
  }
}

async function restoreSnapshot(snapshot) {
  if (!canRestore(snapshot) || busy.value) return
  const confirmed = window.confirm(`Restore snapshot ${snapshot.id} to this stopped/failed Instance? Current VM state will be replaced. This local snapshot is not an off-host backup.`)
  if (!confirmed) return
  busy.value = `restore:${snapshot.id}`
  actionMessage.value = ''
  actionError.value = ''
  try {
    const result = await post(`/instances/${encodeURIComponent(instanceId.value)}/snapshots/${encodeURIComponent(snapshot.id)}/restore`, {
      idempotency_key: idempotencyKey(),
    })
    await load()
    actionMessage.value = `Snapshot restore queued as operation ${result.operation.id}. The agent will verify the artifact before restoring.`
  } catch (err) {
    actionError.value = err.message || 'Could not queue snapshot restore'
  } finally {
    busy.value = ''
  }
}

onMounted(async () => {
  try {
    projects.value = await listAll('/projects')
    if (projects.value.length) {
      projectId.value = projects.value[0].id
      await load()
    } else {
      error.value = 'No projects yet — snapshots and backup metadata are scoped per project.'
    }
  } catch (err) {
    error.value = err.message || 'Failed to load projects'
  }
})
</script>

<style scoped>
.notice { margin-top:16px;padding:14px 16px;font-size:13px;color:var(--ink2);line-height:1.55 }
.notice strong { color:var(--ink) }
.catalog { padding:0;margin-top:14px;overflow-x:auto }
.section-head { display:flex;justify-content:space-between;align-items:center;gap:10px;flex-wrap:wrap;padding:14px 16px }
.section-head h2 { margin:0 0 4px;font-size:15px }
.section-head .muted { font-size:12px }
.error-bar { margin:12px 16px;padding:10px 12px;border-radius:var(--r2);background:rgba(255,69,58,.09);color:#C22A22;font-size:13px }
.action-message { margin:10px 16px }
.action-row { display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap;padding:0 16px 14px }
.action-hint { max-width:220px;font-size:11px;margin-top:4px }
</style>
