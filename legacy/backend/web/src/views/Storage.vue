<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div><h1>Persistent Storage &amp; Volumes</h1><p class="muted">Volume declarations only. No disk is provisioned, mounted, or attached.</p></div>
      <div class="row"><select v-model="projectId" style="width:auto" @change="load"><option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option></select><button :disabled="!projectId || !services.length" @click="openCreate">+ Declare Volume</button></div>
    </div>
    <div v-if="error" class="error-bar">{{ error }}</div>
    <div v-if="!error" class="card notice">These are metadata-only declarations, not provisioned disks. Capacity usage, attachment, health, resize, clone, and deletion operations are unavailable.</div>
    <div class="grid kpis" v-if="!error"><div class="card kpi"><span class="kpi-label">Declared Capacity</span><div class="kpi-value">{{ totalGib }} <small>GiB</small></div><span class="muted">{{ volumes.length }} declarations · usage unavailable</span></div><div class="card kpi"><span class="kpi-label">Provisioning</span><div class="kpi-value"><span class="pill info">Metadata only</span></div><span class="muted">No backing disk is created</span></div></div>
    <div class="card" style="padding:0;margin-top:14px;overflow-x:auto" v-if="!error">
      <table v-if="volumes.length"><thead><tr><th>Volume</th><th>Capacity</th><th>Service</th><th>Mount path</th><th>Node affinity</th><th>Access</th><th>Declared</th><th>State / integrity</th></tr></thead>
        <tbody><tr v-for="v in volumes" :key="v.id"><td><div>{{ v.name || '(unnamed)' }}</div><div class="mono muted">{{ v.id }}</div></td><td>{{ mib(v.size_mib) }}</td><td class="mono">{{ serviceName(v.service_id) }}</td><td class="mono muted">{{ v.mount_path || '—' }}</td><td class="mono muted">{{ v.node_affinity_id || 'Not specified' }}</td><td>{{ v.access_mode || '—' }}</td><td class="muted">{{ ago(v.created_at) }}</td><td><span class="pill info">{{ v.state || 'metadata_only' }}</span><div class="muted" style="font-size:11px">Integrity: {{ v.integrity_status || 'not_available' }}</div></td></tr></tbody>
      </table><div v-else class="empty">No volume declarations in this project yet.</div>
    </div>
    <div v-if="createOpen" class="modal" @click.self="createOpen = false"><form class="card modal-box" @submit.prevent="create"><h2>Declare metadata-only volume</h2><label class="fld">Service<select v-model="form.service_id" required><option value="" disabled>Select service…</option><option v-for="s in services" :key="s.id" :value="s.id">{{ s.name || s.id }}</option></select></label><label class="fld">Name<input v-model="form.name" placeholder="api-data" required /></label><label class="fld">Size (GiB)<input v-model.number="form.gib" type="number" min="1" max="16384" required /></label><label class="fld">Mount path (optional)<input v-model="form.mount_path" placeholder="/var/lib/app" /></label><label class="fld">Access mode<input value="single_writer" disabled /></label><label class="fld">Node affinity (optional)<select v-model="form.node_affinity_id"><option value="">Not specified</option><option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.name || n.id }} · {{ n.id }}</option></select><span v-if="!nodes.length" class="muted">No Fleet-node list available; affinity is stored only if you provide an ID.</span></label><div v-if="formErr" class="error-bar">{{ formErr }}</div><p class="muted">Creating this record does not provision or attach a disk.</p><div class="row" style="justify-content:flex-end;margin-top:12px"><button type="button" class="ghost" @click="createOpen = false">Cancel</button><button :disabled="busy">{{ busy ? 'Saving…' : 'Save declaration' }}</button></div></form></div>
  </div>
</template>
<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, post, listAll, ago } from '../lib/api.js'
const projects = ref([]), projectId = ref(''), volumes = ref([]), services = ref([]), nodes = ref([])
const error = ref(''), formErr = ref(''), busy = ref(false), createOpen = ref(false)
const form = ref({ service_id: '', name: '', gib: 10, mount_path: '', node_affinity_id: '' })
const totalGib = computed(() => (volumes.value.reduce((sum, v) => sum + (v.size_mib || 0), 0) / 1024).toFixed(1))
const mib = (n) => n >= 1024 ? `${(n / 1024).toFixed(n % 1024 ? 1 : 0)} GiB` : `${n || 0} MiB`
const serviceName = (id) => services.value.find((s) => s.id === id)?.name || id || '—'
async function load() {
  if (!projectId.value) return
  error.value = ''
  try {
    const pid = encodeURIComponent(projectId.value)
    const [v, s] = await Promise.all([listAll(`/projects/${pid}/volumes`), listAll(`/projects/${pid}/services`),])
    volumes.value = v; services.value = s
    if (form.value.service_id && !s.some((item) => item.id === form.value.service_id)) form.value.service_id = ''
  } catch (e) { error.value = e.message || 'Failed to load volume declarations' }
}
function openCreate() { formErr.value = ''; form.value = { service_id: services.value[0]?.id || '', name: '', gib: 10, mount_path: '', node_affinity_id: '' }; createOpen.value = true }
async function create() {
  formErr.value = ''
  if (!form.value.name.trim() || !form.value.service_id) { formErr.value = 'Select a service and enter a name.'; return }
  busy.value = true
  try {
    await post(`/projects/${encodeURIComponent(projectId.value)}/services/${encodeURIComponent(form.value.service_id)}/volumes`, { name: form.value.name.trim(), size_mib: Math.max(1, form.value.gib || 1) * 1024, ...(form.value.mount_path.trim() ? { mount_path: form.value.mount_path.trim() } : {}), access_mode: 'single_writer', ...(form.value.node_affinity_id ? { node_affinity_id: form.value.node_affinity_id } : {}) })
    createOpen.value = false; await load()
  } catch (e) { formErr.value = e.message || 'Could not save declaration' } finally { busy.value = false }
}
onMounted(async () => {
  try { projects.value = await listAll('/projects'); nodes.value = await listAll('/fleet/nodes').catch(() => []); if (projects.value.length) { projectId.value = projects.value[0].id; await load() } else error.value = 'No projects yet — create a project first.' }
  catch (e) { error.value = e.message || 'Failed to load projects' }
})
</script>
<style scoped>
.kpis { grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); margin-top: 16px }.kpi { display:flex;flex-direction:column;gap:4px }.kpi-label { font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:var(--ink3) }.kpi-value { font-size:24px;font-weight:600 }.kpi-value small { font-size:13px;font-weight:400;color:var(--ink2) }.notice { margin-top:14px;color:var(--ink2);font-size:13px }.error-bar { margin-top:12px;padding:10px 12px;border-radius:var(--r2);background:rgba(255,69,58,.09);color:#C22A22;font-size:13px }.fld { display:block;font-size:12px;color:var(--ink2);margin-top:10px }.fld input,.fld select { margin-top:4px }.modal { position:fixed;inset:0;background:rgba(0,0,0,.35);display:flex;align-items:center;justify-content:center;z-index:40;overflow:auto;padding:16px }.modal-box { width:420px;max-width:92vw;max-height:94vh;overflow:auto }
</style>
