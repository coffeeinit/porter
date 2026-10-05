<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Backups &amp; Recovery</h1>
        <p class="muted">Per-project backup schedules and the durable snapshot / backup catalog with checksums.</p>
      </div>
      <select v-model="projectId" style="width:auto" @change="load">
        <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>
    <template v-else>
      <!-- Schedules -->
      <div class="card" style="padding:0;margin-top:16px;overflow-x:auto">
        <div class="row" style="justify-content:space-between;padding:14px 16px;flex-wrap:wrap;gap:8px">
          <div>
            <h2 style="margin:0">Backup Schedules</h2>
            <span class="muted" style="font-size:12px">Cron-driven recurring backups per workload (one per project + workload).</span>
          </div>
          <button @click="schedOpen = !schedOpen">{{ schedOpen ? 'Close' : '+ New Schedule' }}</button>
        </div>

        <form v-if="schedOpen" class="sched-form" @submit.prevent="createSchedule">
          <label class="fld">Workload<input v-model="sched.workload" placeholder="postgres-primary" /></label>
          <label class="fld">Cron (5 fields)<input v-model="sched.cron" placeholder="0 2 * * *" class="mono" /></label>
          <label class="fld">Retention (copies)<input v-model.number="sched.retention" type="number" min="1" style="width:90px" /></label>
          <button type="submit" :disabled="busy">{{ busy ? 'Saving…' : 'Save Schedule' }}</button>
          <span v-if="schedMsg" :class="schedErr ? 'bad' : 'oktext'">{{ schedMsg }}</span>
        </form>

        <table v-if="schedules.length">
          <thead>
            <tr>
              <th>Workload</th>
              <th>Schedule</th>
              <th>Retention</th>
              <th>Last Run</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in schedules" :key="s.ID || s.id">
              <td class="mono">{{ s.Workload || s.workload }}</td>
              <td class="mono">{{ s.Cron || s.cron }}</td>
              <td>{{ s.Retention || s.retention }} copies</td>
              <td class="muted">{{ lastRun(s) }}</td>
              <td><span class="pill" :class="enabled(s) ? 'ok' : 'info'">{{ enabled(s) ? 'Active' : 'Disabled' }}</span></td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">No backup schedules for this project yet — the service templates add a daily default when provisioned.</div>
      </div>

      <!-- Backup catalog -->
      <div class="card" style="padding:0;margin-top:14px;overflow-x:auto">
        <div style="padding:14px 16px">
          <h2 style="margin:0">Backup Catalog</h2>
          <span class="muted" style="font-size:12px">Immutable recovery points with SHA-256 digests, newest first.</span>
        </div>
        <table v-if="backups.length">
          <thead>
            <tr>
              <th>Backup ID</th>
              <th>Object</th>
              <th>Size</th>
              <th>SHA-256</th>
              <th>Trigger</th>
              <th>Created</th>
              <th>Integrity</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="b in backups" :key="b.ID || b.id">
              <td class="mono">{{ short(b.ID || b.id) }}</td>
              <td class="mono muted">{{ b.ObjectPath || b.object_path }}</td>
              <td>{{ bytes(b.SizeBytes || b.size_bytes) }}</td>
              <td class="mono muted" :title="b.SHA256 || b.sha256">{{ (b.SHA256 || b.sha256 || '').slice(0, 12) }}…</td>
              <td><span class="pill info">{{ b.Trigger || b.trigger || 'manual' }}</span></td>
              <td class="muted">{{ ago(b.CreatedAt || b.created_at) }}</td>
              <td>
                <span class="pill" :class="(b.Verified ?? b.verified) ? 'ok' : 'warn'">
                  {{ (b.Verified ?? b.verified) ? 'Verified' : 'Unverified' }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">No backups recorded for this project yet — schedules will populate the catalog on their first run.</div>
      </div>

      <!-- Snapshots (per replica) -->
      <div class="card" style="padding:0;margin-top:14px;overflow-x:auto">
        <div class="row" style="justify-content:space-between;padding:14px 16px;flex-wrap:wrap;gap:8px">
          <div>
            <h2 style="margin:0">MicroVM Snapshots</h2>
            <span class="muted" style="font-size:12px">Firecracker state + memory snapshots per replica.</span>
          </div>
          <select v-model="replicaId" style="width:auto">
            <option value="">Select replica…</option>
            <option v-for="r in replicas" :key="r.id" :value="r.id">
              {{ r.service_name || r.name || 'vm' }} · #{{ r.replica_index ?? '?' }}
            </option>
          </select>
        </div>
        <div v-if="snapErr" class="error-bar" style="margin:0 16px 12px">{{ snapErr }}</div>
        <table v-if="snapshots.length">
          <thead>
            <tr>
              <th>Snapshot ID</th>
              <th>Type</th>
              <th>State Object</th>
              <th>Size</th>
              <th>Created</th>
              <th>Verified</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in snapshots" :key="s.ID || s.id">
              <td class="mono">{{ short(s.ID || s.id) }}</td>
              <td><span class="pill info">{{ s.Type || s.type || 'full' }}</span></td>
              <td class="mono muted">{{ s.StateObject || s.state_object || '—' }}</td>
              <td>{{ bytes(s.SizeBytes || s.size_bytes) }}</td>
              <td class="muted">{{ ago(s.CreatedAt || s.created_at) }}</td>
              <td>
                <span class="pill" :class="(s.Verified ?? s.verified) ? 'ok' : 'warn'">
                  {{ (s.Verified ?? s.verified) ? 'Verified' : 'Pending' }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="replicaId && !snapErr" class="empty">No snapshots for this replica yet — take one from the replica controls on the application page.</div>
        <div v-else-if="!replicaId" class="empty">Pick a replica to inspect its snapshots.</div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { get, post, ago } from '../lib/api.js'

const projects = ref([])
const projectId = ref('')
const schedules = ref([])
const backups = ref([])
const replicas = ref([])
const replicaId = ref('')
const snapshots = ref([])
const error = ref('')
const snapErr = ref('')
const schedOpen = ref(false)
const busy = ref(false)
const sched = ref({ workload: '', cron: '0 2 * * *', retention: 3 })
const schedMsg = ref('')
const schedErr = ref(false)

let poll = null
const list = (d) => (Array.isArray(d) ? d : d?.schedules || d?.backups || d?.snapshots || d?.items || [])
const short = (s) => (String(s).length > 18 ? `${String(s).slice(0, 15)}…` : s)
const bytes = (n) => {
  if (!n) return '—'
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(1)} GB`
  if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`
  return `${n} B`
}
const enabled = (s) => s.Enabled ?? s.enabled ?? true
const lastRun = (s) => {
  const t = s.LastRun || s.last_run
  return t ? ago(t) : 'never'
}

async function load() {
  if (!projectId.value) return
  error.value = ''
  try {
    const [s, b, r] = await Promise.all([
      get(`/projects/${projectId.value}/backups/schedules`),
      get(`/projects/${projectId.value}/backups`),
      get(`/projects/${projectId.value}/replicas?limit=200`).catch(() => []),
    ])
    schedules.value = list(s)
    backups.value = list(b)
    replicas.value = Array.isArray(r) ? r : []
    if (replicaId.value && !replicas.value.some((x) => x.id === replicaId.value)) {
      replicaId.value = ''
      snapshots.value = []
    }
    if (replicaId.value) await loadSnapshots()
  } catch (e) {
    error.value = e.message || 'failed to load backups'
  }
}

async function loadSnapshots() {
  snapErr.value = ''
  try {
    const d = await get(`/vms/${replicaId.value}/snapshots`)
    snapshots.value = list(d)
  } catch (e) {
    snapshots.value = []
    snapErr.value = e.message || 'failed to load snapshots'
  }
}

async function createSchedule() {
  schedMsg.value = ''
  busy.value = true
  try {
    await post(`/projects/${projectId.value}/backups/schedules`, {
      workload: sched.value.workload.trim(),
      cron: sched.value.cron.trim(),
      retention: Math.max(1, sched.value.retention || 3),
    })
    schedMsg.value = 'Schedule saved.'
    schedErr.value = false
    sched.value = { workload: '', cron: '0 2 * * *', retention: 3 }
    const s = await get(`/projects/${projectId.value}/backups/schedules`)
    schedules.value = list(s)
  } catch (e) {
    schedMsg.value = e.message || 'failed to save schedule'
    schedErr.value = true
  } finally { busy.value = false }
}

onMounted(async () => {
  try {
    const d = await get('/projects?limit=200')
    projects.value = Array.isArray(d) ? d : []
    if (projects.value.length) {
      projectId.value = projects.value[0].id
      await load()
    } else {
      error.value = 'No projects yet — schedules and backups are scoped per project.'
    }
  } catch (e) { error.value = e.message || 'failed to load projects' }
  poll = setInterval(() => { if (projectId.value) load() }, 15000)
})

onUnmounted(() => { if (poll) clearInterval(poll) })
</script>

<style scoped>
.sched-form { display: flex; gap: 10px; align-items: flex-end; flex-wrap: wrap; padding: 0 16px 14px }
.fld { display: flex; flex-direction: column; font-size: 12px; color: var(--ink2); gap: 4px }
.error-bar { padding: 10px 12px; border-radius: var(--r2); background: rgba(255, 69, 58, .09); color: #C22A22; font-size: 13px }
.bad { color: #C22A22; font-size: 12px }
.oktext { color: #1B873B; font-size: 12px }
h2 { font-size: 15px }
</style>
