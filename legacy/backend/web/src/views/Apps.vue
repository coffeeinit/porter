<template>
  <div class="apps">
    <div class="page-head">
      <div>
        <div class="row" style="gap:10px;flex-wrap:wrap">
          <h1>Applications</h1>
          <span v-if="!loading && !err" class="pill info">{{ projects.length }} total</span>
          <span v-if="runningCount" class="pill ok">{{ runningCount }} running</span>
        </div>
        <p class="muted sub">Deploy code, inspect MicroVM runtime health, and manage service lifecycles.</p>
      </div>
      <router-link to="/projects" class="create">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" class="ic"><path d="M12 5v14M5 12h14"/></svg>
        New application
      </router-link>
    </div>

    <div v-if="err" class="errbar">{{ err }} <button class="ghost" @click="load">Retry</button></div>

    <!-- Toolbar -->
    <div class="card toolbar">
      <input v-model="q" class="search" placeholder="Filter by application name, image, or tag…" />
      <span class="muted count-note">Showing <strong>{{ filtered.length }}</strong> of {{ projects.length }}</span>
      <div class="mode">
        <button :class="{ active: view === 'grid' }" @click="view = 'grid'">Grid</button>
        <button :class="{ active: view === 'list' }" @click="view = 'list'">List</button>
      </div>
    </div>

    <div v-if="loading" class="empty">Loading applications…</div>
    <div v-else-if="filtered.length === 0 && !err" class="empty">
      {{ q ? `No applications match “${q}”.` : 'No applications yet — create your first one from Projects.' }}
    </div>

    <!-- Grid view -->
    <div v-else-if="view === 'grid'" class="grid-cards">
      <router-link v-for="p in filtered" :key="p.id" class="card app-card" :to="`/apps/${p.id}`">
        <div class="row" style="justify-content:space-between">
          <span class="avatar">{{ initials(p.name) }}</span>
          <span class="pill" :class="pillClass(statusOf(p))">{{ statusOf(p) }}</span>
        </div>
        <h4>{{ p.name }}</h4>
        <p class="mono dim">{{ p.image || p.source || 'no image' }}</p>
        <div class="meta-row">
          <span>{{ (p.vm_ids || []).length }}/{{ p.replicas_desired || p.replicas || 1 }} replicas</span>
          <span class="muted">{{ ago(p.created_at) }}</span>
        </div>
        <div v-if="(p.tags || []).length" class="tag-row">
          <span v-for="t in p.tags" :key="t" class="tag">{{ t }}</span>
        </div>
      </router-link>
    </div>

    <!-- List view -->
    <div v-else class="card">
      <table>
        <thead>
          <tr><th>Application</th><th>Status</th><th>Image</th><th>Replicas</th><th>Created</th></tr>
        </thead>
        <tbody>
          <tr v-for="p in filtered" :key="p.id" class="app-row" @click="$router.push(`/apps/${p.id}`)">
            <td><strong>{{ p.name }}</strong> <span v-if="(p.tags || []).length" class="muted">· {{ p.tags.join(', ') }}</span></td>
            <td><span class="pill" :class="pillClass(statusOf(p))">{{ statusOf(p) }}</span></td>
            <td class="mono dim">{{ p.image || '—' }}</td>
            <td class="tnum">{{ (p.vm_ids || []).length }}/{{ p.replicas_desired || p.replicas || 1 }}</td>
            <td class="muted">{{ ago(p.created_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, ago, pillClass } from '../lib/api.js'

const loading = ref(true)
const err = ref('')
const projects = ref([])
const vms = ref([])
const q = ref('')
const view = ref('grid')

const filtered = computed(() => {
  const s = q.value.trim().toLowerCase()
  if (!s) return projects.value
  return projects.value.filter((p) =>
    [p.name, p.image, p.source, ...(p.tags || [])].some((v) => String(v || '').toLowerCase().includes(s)))
})
const runningCount = computed(() => projects.value.filter((p) => statusOf(p) === 'running').length)

const vmStates = computed(() => {
  const m = new Map()
  for (const vm of vms.value) {
    if (!m.has(vm.project_id)) m.set(vm.project_id, [])
    m.get(vm.project_id).push(vm.state)
  }
  return m
})

// Status derived from the real VM pool behind each project — no invented states.
function statusOf(p) {
  const pool = vmStates.value.get(p.id) || []
  if (!pool.length) return (p.vm_ids || []).length ? 'pending' : 'idle'
  if (pool.some((s) => s === 'failed' || s === 'crashed')) return 'failed'
  if (pool.every((s) => s === 'running')) return 'running'
  if (pool.some((s) => s === 'running')) return 'degraded'
  return 'pending'
}

function initials(name) {
  return String(name || '?').replace(/[^a-zA-Z0-9]/g, '').slice(0, 2).toUpperCase() || '?'
}

async function load() {
  loading.value = true
  err.value = ''
  const failed = []
  await Promise.all([
    get('/projects').then((d) => { projects.value = Array.isArray(d) ? d : [] }).catch(() => failed.push('projects')),
    get('/vms').then((d) => { vms.value = Array.isArray(d) ? d : [] }).catch(() => failed.push('vms')),
  ])
  if (failed.length) err.value = `Failed to load: ${failed.join(', ')}.`
  loading.value = false
}

onMounted(load)
</script>

<style scoped>
.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:16px;margin-bottom:16px;flex-wrap:wrap}
h1{margin:0}
.sub{font-size:12.5px;margin-top:4px}
.create{display:inline-flex;align-items:center;gap:6px;background:var(--blue);color:#fff;border-radius:var(--r2);padding:8px 14px;font-weight:550;font-size:13px}
.create:hover{background:var(--blue2)}
.ic{width:15px;height:15px}
.toolbar{display:flex;align-items:center;gap:14px;padding:10px 14px;margin-bottom:16px;flex-wrap:wrap}
.toolbar input.search{max-width:380px}
.count-note{font-size:11.5px;margin-left:auto}
.count-note strong{color:var(--ink)}
.mode{display:flex;padding:2px;background:rgba(0,0,0,.045);border-radius:8px}
.mode button{font-size:11px;padding:5px 11px;border-radius:6px;color:var(--ink2);background:transparent;font-weight:500}
.mode button.active{background:#fff;color:var(--ink);box-shadow:0 1px 2px rgba(0,0,0,.08);font-weight:600}
.grid-cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(250px,1fr));gap:12px}
.app-card{display:block;color:inherit;padding:16px;transition:transform .14s,box-shadow .14s,border-color .14s}
.app-card:hover{transform:translateY(-2px);box-shadow:0 8px 24px rgba(0,0,0,.06);border-color:rgba(0,113,227,.28)}
.avatar{width:32px;height:32px;border-radius:9px;background:linear-gradient(160deg,#777,#333);color:#fff;display:grid;place-items:center;font-size:11px;font-weight:650}
.app-card h4{font-size:13.5px;font-weight:600;margin:10px 0 2px}
.dim{color:var(--ink3)}
.app-card .mono{font-size:11px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.meta-row{display:flex;justify-content:space-between;font-size:11px;color:var(--ink2);margin-top:10px;font-variant-numeric:tabular-nums}
.tag-row{display:flex;gap:5px;flex-wrap:wrap;margin-top:8px}
.tag{font-size:10px;padding:1px 7px;border-radius:6px;background:rgba(0,0,0,.05);color:var(--ink2)}
.app-row{cursor:pointer}
.app-row:hover td{background:rgba(0,0,0,.018)}
.tnum{font-variant-numeric:tabular-nums}
.errbar{display:flex;align-items:center;gap:10px;background:rgba(255,69,58,.08);border:1px solid rgba(255,69,58,.25);color:#C22A22;border-radius:var(--r2);padding:9px 12px;font-size:12.5px;margin-bottom:14px}
.errbar button{padding:4px 10px;font-size:12px}
</style>
