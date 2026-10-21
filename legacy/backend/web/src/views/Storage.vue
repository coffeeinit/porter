<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Persistent Storage &amp; Volumes</h1>
        <p class="muted">Dedicated block storage volumes backed by real host directories, with clone and online resize.</p>
      </div>
      <div class="row">
        <select v-model="projectId" style="width:auto" @change="load">
          <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
        <button @click="createOpen = true">+ Create Volume</button>
      </div>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>

    <div class="grid kpis" v-if="!error">
      <div class="card kpi">
        <span class="kpi-label">Total Allocated</span>
        <div class="kpi-value">{{ totalGib }} <small>GiB</small></div>
        <span class="muted">across {{ volumes.length }} volumes</span>
      </div>
      <div class="card kpi">
        <span class="kpi-label">Used on Host</span>
        <div class="kpi-value">{{ usedGib }} <small>GiB</small></div>
        <div class="bar"><div :style="{ width: usedPct + '%' }"></div></div>
        <span class="muted">{{ usedPct }}% of allocated</span>
      </div>
      <div class="card kpi">
        <span class="kpi-label">Attached</span>
        <div class="kpi-value">{{ attachedCount }} <small>/ {{ volumes.length }}</small></div>
        <span class="muted">volumes mounted by a replica</span>
      </div>
      <div class="card kpi">
        <span class="kpi-label">Pool Status</span>
        <div class="kpi-value"><span class="pill ok" style="font-size:13px">Healthy</span></div>
        <span class="muted">host directory engine · ext4</span>
      </div>
    </div>

    <div class="card" style="padding:0;margin-top:14px;overflow-x:auto" v-if="!error">
      <table v-if="volumes.length">
        <thead>
          <tr>
            <th>Volume</th>
            <th>Capacity</th>
            <th>Mount Path</th>
            <th>Attached Replica</th>
            <th>Created</th>
            <th>Status</th>
            <th style="text-align:right">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="v in volumes" :key="v.id">
            <td>
              <div class="row" style="gap:8px">
                <span class="vol-ico">▤</span>
                <div>
                  <div>{{ v.name || '(unnamed)' }}</div>
                  <div class="mono muted">{{ v.id }}</div>
                </div>
              </div>
            </td>
            <td style="min-width:150px">
              <div class="row" style="justify-content:space-between;font-size:12px">
                <span>{{ mib(v.size_mib) }}</span>
                <span class="muted">{{ usageText(v) }}</span>
              </div>
              <div class="bar"><div :style="{ width: usagePct(v) + '%' }"></div></div>
            </td>
            <td class="mono muted">{{ v.path || '—' }}</td>
            <td>
              <template v-if="attached(v)">
                <div>{{ attached(v).service_name || attached(v).name }}</div>
                <div class="mono muted">{{ attached(v).id }}</div>
              </template>
              <span v-else class="muted">unattached</span>
            </td>
            <td class="muted">{{ ago(v.created_at) }}</td>
            <td>
              <span class="pill" :class="attached(v) ? 'ok' : 'info'">{{ attached(v) ? 'Attached' : 'Available' }}</span>
            </td>
            <td style="text-align:right;white-space:nowrap">
              <button class="ghost mini" @click="openResize(v)">Resize</button>
              <button class="ghost mini" @click="clone(v)">Clone</button>
              <button class="danger mini" @click="remove(v)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No volumes in this project yet — create one to attach persistent storage to a replica.</div>
    </div>

    <!-- Create modal -->
    <div v-if="createOpen" class="modal" @click.self="createOpen = false">
      <div class="card modal-box">
        <h2>Create Volume</h2>
        <label class="fld">Name<input v-model="form.name" placeholder="api-data" /></label>
        <label class="fld">Size (GiB)<input v-model.number="form.gib" type="number" min="1" /></label>
        <label class="fld">Mount path (optional)<input v-model="form.mount" placeholder="/var/lib/app" /></label>
        <div v-if="formErr" class="error-bar">{{ formErr }}</div>
        <div class="row" style="justify-content:flex-end;margin-top:12px">
          <button class="ghost" @click="createOpen = false">Cancel</button>
          <button :disabled="busy" @click="create">{{ busy ? 'Creating…' : 'Create' }}</button>
        </div>
      </div>
    </div>

    <!-- Resize modal -->
    <div v-if="resizeVol" class="modal" @click.self="resizeVol = null">
      <div class="card modal-box">
        <h2>Resize {{ resizeVol.name }}</h2>
        <p class="muted mono" style="font-size:12px">{{ resizeVol.id }}</p>
        <label class="fld">New size (GiB)<input v-model.number="resizeGib" type="number" min="1" /></label>
        <div v-if="formErr" class="error-bar">{{ formErr }}</div>
        <div class="row" style="justify-content:flex-end;margin-top:12px">
          <button class="ghost" @click="resizeVol = null">Cancel</button>
          <button :disabled="busy" @click="resize">{{ busy ? 'Resizing…' : 'Resize' }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, post, del, ago } from '../lib/api.js'

const projects = ref([])
const projectId = ref('')
const volumes = ref([])
const replicas = ref([])
const usage = ref({})
const error = ref('')
const formErr = ref('')
const busy = ref(false)
const createOpen = ref(false)
const resizeVol = ref(null)
const resizeGib = ref(1)
const form = ref({ name: '', gib: 10, mount: '' })

const list = (d) => (Array.isArray(d) ? d : d?.volumes || d?.items || [])

async function load() {
  if (!projectId.value) return
  error.value = ''
  try {
    const [vols, reps] = await Promise.all([
      get(`/projects/${projectId.value}/volumes?limit=200`),
      get(`/projects/${projectId.value}/replicas?limit=200`).catch(() => []),
    ])
    volumes.value = list(vols)
    replicas.value = list(reps)
    // Real host-side usage; tolerate per-volume failures.
    const results = await Promise.allSettled(
      volumes.value.slice(0, 24).map((v) => get(`/projects/${projectId.value}/volumes/${v.id}/usage`))
    )
    const u = {}
    volumes.value.forEach((v, i) => {
      if (results[i]?.status === 'fulfilled') u[v.id] = results[i].value
    })
    usage.value = u
  } catch (e) {
    error.value = e.message || 'failed to load volumes'
  }
}

const attached = (v) => replicas.value.find((r) => r.volume_id === v.id)
const mib = (n) => (n >= 1024 ? `${(n / 1024).toFixed(n % 1024 ? 1 : 0)} GiB` : `${n || 0} MiB`)
const usagePct = (v) => {
  const u = usage.value[v.id]
  if (!u?.limit_bytes) return 0
  return Math.min(100, Math.round(((u.used_bytes || 0) / u.limit_bytes) * 100))
}
const usageText = (v) => {
  const u = usage.value[v.id]
  if (!u) return '—'
  const gb = (b) => `${(b / 1024 ** 3).toFixed(1)} GB`
  return `${gb(u.used_bytes || 0)} (${usagePct(v)}%)`
}
const totalGib = computed(() => (volumes.value.reduce((s, v) => s + (v.size_mib || 0), 0) / 1024).toFixed(1))
const usedGib = computed(() => (volumes.value.reduce((s, v) => s + (usage.value[v.id]?.used_bytes || 0), 0) / 1024 ** 3).toFixed(1))
const usedPct = computed(() => {
  const total = volumes.value.reduce((s, v) => s + (v.size_mib || 0) * 1024 * 1024, 0)
  return total ? Math.min(100, Math.round((volumes.value.reduce((s, v) => s + (usage.value[v.id]?.used_bytes || 0), 0) / total) * 100)) : 0
})
const attachedCount = computed(() => volumes.value.filter(attached).length)

async function create() {
  formErr.value = ''
  if (!form.value.name.trim()) { formErr.value = 'name is required'; return }
  busy.value = true
  try {
    await post(`/projects/${projectId.value}/volumes`, {
      name: form.value.name.trim(),
      size_mib: Math.max(1, form.value.gib || 1) * 1024,
      mount_path: form.value.mount.trim(),
    })
    createOpen.value = false
    form.value = { name: '', gib: 10, mount: '' }
    await load()
  } catch (e) { formErr.value = e.message || 'create failed' } finally { busy.value = false }
}

function openResize(v) {
  resizeVol.value = v
  resizeGib.value = Math.max(1, Math.round((v.size_mib || 1024) / 1024))
  formErr.value = ''
}

async function resize() {
  formErr.value = ''
  busy.value = true
  try {
    await post(`/projects/${projectId.value}/volumes/${resizeVol.value.id}/resize`, {
      size_mib: Math.max(1, resizeGib.value || 1) * 1024,
    })
    resizeVol.value = null
    await load()
  } catch (e) { formErr.value = e.message || 'resize failed' } finally { busy.value = false }
}

async function clone(v) {
  error.value = ''
  try {
    await post(`/projects/${projectId.value}/volumes/${v.id}/clone`)
    await load()
  } catch (e) { error.value = e.message || 'clone failed' }
}

async function remove(v) {
  if (!confirm(`Delete volume "${v.name}" (${v.id})? The host backing directory is removed.`)) return
  error.value = ''
  try {
    await del(`/projects/${projectId.value}/volumes/${v.id}`)
    await load()
  } catch (e) { error.value = e.message || 'delete failed' }
}

onMounted(async () => {
  try {
    projects.value = list(await get('/projects?limit=200'))
    if (projects.value.length) {
      projectId.value = projects.value[0].id
      await load()
    } else {
      error.value = 'No projects yet — create a project first, then add volumes to it.'
    }
  } catch (e) { error.value = e.message || 'failed to load projects' }
})
</script>

<style scoped>
.kpis { grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); margin-top: 16px }
.kpi { display: flex; flex-direction: column; gap: 4px }
.kpi-label { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3) }
.kpi-value { font-size: 24px; font-weight: 600; letter-spacing: -.02em }
.kpi-value small { font-size: 13px; font-weight: 400; color: var(--ink2) }
.bar { height: 6px; border-radius: 999px; background: var(--soft); overflow: hidden; margin: 4px 0 }
.bar > div { height: 100%; border-radius: 999px; background: var(--blue) }
.vol-ico { width: 26px; height: 26px; display: inline-flex; align-items: center; justify-content: center; border-radius: 8px; background: var(--solid); color: var(--ink2) }
button.ghost.mini, button.danger.mini { padding: 4px 10px; font-size: 12px; margin-left: 4px }
.error-bar { margin-top: 12px; padding: 10px 12px; border-radius: var(--r2); background: rgba(255, 69, 58, .09); color: #C22A22; font-size: 13px }
.fld { display: block; font-size: 12px; color: var(--ink2); margin-top: 10px }
.fld input { margin-top: 4px }
.modal { position: fixed; inset: 0; background: rgba(0, 0, 0, .35); display: flex; align-items: center; justify-content: center; z-index: 40 }
.modal-box { width: 380px; max-width: 92vw }
</style>
