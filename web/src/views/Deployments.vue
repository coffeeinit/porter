<template>
  <div class="deps">
    <div class="page-head">
      <div>
        <h1>Deployments</h1>
        <p class="muted sub">Releases, build logs, preview URLs and rollbacks across your applications.</p>
      </div>
      <label class="proj-pick">
        <span class="muted">Application</span>
        <select v-model="projectId">
          <option value="__all__">All applications</option>
          <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
      </label>
    </div>

    <div v-if="listErr" class="errbar">{{ listErr }} <button class="ghost" @click="loadDeployments">Retry</button></div>

    <div class="split">
      <!-- Deployment list -->
      <div class="list-col">
        <div v-if="loading" class="empty">Loading deployments…</div>
        <div v-else-if="!deployments.length && !listErr" class="empty">
          No deployments yet for this application.
        </div>
        <div
          v-else
          v-for="d in deployments"
          :key="d.id"
          class="card dep-item"
          :class="{ sel: d.id === selectedId }"
          @click="select(d)"
        >
          <div class="row" style="justify-content:space-between">
            <span class="mono rev">#{{ d.revision }}</span>
            <span class="pill" :class="pillClass(d.build_status)">{{ d.build_status || '—' }}</span>
          </div>
          <div class="dep-title">{{ d.version_label || shortId(d.id) }}</div>
          <div class="row meta" style="justify-content:space-between">
            <span>{{ d.environment || 'preview' }} · {{ d.route_weight ?? 0 }}% traffic</span>
            <span class="muted">{{ ago(d.created_at) }}</span>
          </div>
        </div>
      </div>

      <!-- Detail panel -->
      <div class="detail-col">
        <div v-if="!selected" class="empty">Select a deployment to inspect its build logs and rollout state.</div>
        <template v-else>
          <div class="card panel-head">
            <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
              <div>
                <div class="row" style="gap:10px">
                  <h2 style="margin:0">Deployment {{ selected.version_label || shortId(selected.id) }}</h2>
                  <span class="pill" :class="pillClass(selected.build_status)">{{ selected.build_status || '—' }}</span>
                </div>
                <p class="muted sub2">Rev #{{ selected.revision }} · {{ selected.environment || 'preview' }} · {{ projectName }}</p>
              </div>
              <button v-if="canRollback" class="ghost" :disabled="rolling" @click="rollback">
                {{ rolling ? 'Rolling back…' : 'Roll back to previous' }}
              </button>
            </div>
          </div>

          <div v-if="actionMsg" class="okbar">{{ actionMsg }}</div>
          <div v-if="actionErr" class="errbar">{{ actionErr }}</div>

          <h2>Deployment metadata</h2>
          <div class="card">
            <table class="kv">
              <tbody>
                <tr><th>Deployment ID</th><td class="mono">{{ selected.id }}</td></tr>
                <tr><th>Image</th><td class="mono">{{ selected.image_digest || '—' }}</td></tr>
                <tr><th>Guest base</th><td class="mono">{{ selected.guest_base || '—' }}</td></tr>
                <tr><th>Git</th><td class="mono">{{ selected.git_url || '—' }}<template v-if="selected.git_commit"> @ {{ selected.git_commit.slice(0, 8) }}</template></td></tr>
                <tr><th>Route weight</th><td class="tnum">{{ selected.route_weight ?? 0 }}%</td></tr>
                <tr><th>Rollout percent</th><td class="tnum">{{ selected.rollout_percent ?? 0 }}%</td></tr>
                <tr><th>Replica VMs</th><td class="tnum">{{ (selected.vm_ids || []).length }}</td></tr>
                <tr><th>Rollback target</th><td class="mono">{{ selected.rollback_to ? shortId(selected.rollback_to) : 'none' }}</td></tr>
                <tr><th>Created</th><td>{{ fmtDate(selected.created_at) }}</td></tr>
              </tbody>
            </table>
          </div>

          <h2>Preview URL</h2>
          <div class="card">
            <template v-if="selected.preview_url">
              <a :href="previewHref" target="_blank" rel="noopener" class="mono">{{ selected.preview_url }}</a>
              <p class="muted note">Reachable during the preview window before promotion flips production traffic.</p>
            </template>
            <p v-else class="muted note">No preview URL was issued for this deployment.</p>
          </div>

          <h2>Build logs</h2>
          <div v-if="logsLoading" class="empty">Loading build logs…</div>
          <div v-else-if="logErr" class="errbar">{{ logErr }} <button class="ghost" @click="loadLogs">Retry</button></div>
          <div v-else-if="logs.length" class="card log-card">
            <div v-for="(l, i) in logs" :key="i" class="log-line mono">{{ l }}</div>
          </div>
          <div v-else class="empty">No build logs recorded for this deployment.</div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, post, ago, pillClass } from '../lib/api.js'

const loading = ref(true)
const listErr = ref('')
const projects = ref([])
const projectId = ref('')
const deployments = ref([])
const selectedId = ref('')
const logs = ref([])
const logsLoading = ref(false)
const logErr = ref('')
const rolling = ref(false)
const actionMsg = ref('')
const actionErr = ref('')

const selected = computed(() => deployments.value.find((d) => d.id === selectedId.value) || null)
const projectName = computed(() => projectId.value === '__all__' ? 'All applications' : projects.value.find((p) => p.id === projectId.value)?.name || '')
// The backend refuses a rollback when there is no previous deployment; surface
// the button only when a real target exists (its own pointer or an older rev).
const canRollback = computed(() => {
  if (!selected.value) return false
  if (selected.value.rollback_to) return true
  const idx = deployments.value.findIndex((d) => d.id === selected.value.id)
  return idx >= 0 && idx < deployments.value.length - 1
})
const previewHref = computed(() => {
  const u = selected.value?.preview_url || ''
  return /^https?:\/\//.test(u) ? u : `http://${u}`
})

function shortId(id) { return String(id || '').slice(0, 8) || '—' }
function fmtDate(iso) { const t = Date.parse(iso); return t ? new Date(t).toLocaleString() : '—' }

function select(d) {
  if (d.id === selectedId.value) return
  selectedId.value = d.id
  actionMsg.value = ''
  actionErr.value = ''
  loadLogs()
}

async function loadProjects() {
  const ps = await get('/projects')
  projects.value = Array.isArray(ps) ? ps : []
  // '' = All applications (aggregated across the workspace).
  if (!projectId.value) projectId.value = '__all__'
}

async function loadDeployments() {
  loading.value = true
  listErr.value = ''
  try {
    if (projectId.value === '__all__') {
      // Aggregated view: newest deployments across every application.
      const per = await Promise.allSettled(
        projects.value.slice(0, 50).map(async (p) => {
          const ds = await get(`/projects/${encodeURIComponent(p.id)}/deployments?limit=50`)
          return (Array.isArray(ds) ? ds : []).map((d) => ({ ...d, __project: p.name, __pid: p.id }))
        }),
      )
      deployments.value = per.flatMap((r) => (r.status === 'fulfilled' ? r.value : []))
        .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
        .slice(0, 200)
    } else {
      if (!projectId.value) { deployments.value = []; loading.value = false; return }
      const ds = await get(`/projects/${encodeURIComponent(projectId.value)}/deployments?limit=200`)
      deployments.value = Array.isArray(ds) ? ds : []
    }
    // Keep the current selection if it still exists, else default to newest.
    if (!deployments.value.some((d) => d.id === selectedId.value)) {
      selectedId.value = deployments.value.length ? deployments.value[0].id : ''
      logs.value = []
      if (selectedId.value) loadLogs()
    }
  } catch (e) {
    listErr.value = e.message || 'Failed to load deployments.'
  }
  loading.value = false
}

async function loadLogs() {
  if (!projectId.value || !selectedId.value) return
  logsLoading.value = true
  logErr.value = ''
  try {
    const d = await get(`/projects/${encodeURIComponent(projectId.value)}/deployments/${encodeURIComponent(selectedId.value)}/logs`)
    logs.value = (d && Array.isArray(d.logs)) ? d.logs : []
  } catch (e) {
    logErr.value = e.message || 'Failed to load build logs.'
  }
  logsLoading.value = false
}

async function rollback() {
  if (!projectId.value || !selectedId.value) return
  rolling.value = true
  actionMsg.value = ''
  actionErr.value = ''
  try {
    const r = await post(`/projects/${encodeURIComponent(projectId.value)}/deployments/${encodeURIComponent(selectedId.value)}/rollback`)
    const target = r?.deployment ? shortId(r.deployment) : 'previous revision'
    actionMsg.value = `Rolled back to ${target} — replica pool recreated.`
    loadDeployments()
  } catch (e) {
    actionErr.value = e.message || 'Rollback failed.'
  }
  rolling.value = false
}

onMounted(async () => {
  try {
    await loadProjects()
  } catch (e) {
    listErr.value = e.message || 'Failed to load projects.'
    loading.value = false
    return
  }
  loadDeployments()
})
</script>

<style scoped>
.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:16px;margin-bottom:16px;flex-wrap:wrap}
h1{margin:0}
.sub{font-size:12.5px;margin-top:4px}
.proj-pick{display:flex;align-items:center;gap:8px;font-size:12px}
.proj-pick select{width:auto;min-width:180px}
.split{display:grid;grid-template-columns:300px 1fr;gap:16px;align-items:start}
.list-col{display:flex;flex-direction:column;gap:10px}
.dep-item{padding:13px 15px;cursor:pointer;transition:border-color .14s,box-shadow .14s}
.dep-item:hover{border-color:rgba(0,113,227,.28)}
.dep-item.sel{border-color:var(--blue);box-shadow:0 0 0 1px var(--blue)}
.rev{font-weight:600}
.dep-title{font-size:13px;font-weight:600;margin-top:6px}
.meta{font-size:11px;color:var(--ink2);margin-top:5px;font-variant-numeric:tabular-nums}
.detail-col h2{font-size:13.5px;font-weight:650;margin:18px 0 8px}
.panel-head{padding:16px 18px;margin-bottom:4px}
.panel-head h2{margin:0;font-size:15px}
.sub2{font-size:11.5px;margin-top:3px}
.kv th{width:160px;border-bottom:1px solid var(--soft)}
.kv td{border-bottom:1px solid var(--soft)}
.tnum{font-variant-numeric:tabular-nums}
.log-card{background:#161618;color:#E8E8ED;border-color:#161618;padding:14px 16px;max-height:420px;overflow:auto}
.log-line{font-size:12px;line-height:1.6;white-space:pre-wrap;word-break:break-word}
.note{font-size:11.5px;margin-top:8px}
.errbar{display:flex;align-items:center;gap:10px;background:rgba(255,69,58,.08);border:1px solid rgba(255,69,58,.25);color:#C22A22;border-radius:var(--r2);padding:9px 12px;font-size:12.5px;margin-bottom:14px}
.errbar button{padding:4px 10px;font-size:12px}
.okbar{background:rgba(48,209,88,.1);border:1px solid rgba(48,209,88,.3);color:#1B873B;border-radius:var(--r2);padding:9px 12px;font-size:12.5px;margin-bottom:14px}
@media(max-width:900px){.split{grid-template-columns:1fr}}
</style>
