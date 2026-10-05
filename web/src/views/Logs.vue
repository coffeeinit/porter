<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Logs Explorer</h1>
        <p class="muted">Aggregated stdout/stderr telemetry across project replicas, with durable history and full-text search.</p>
      </div>
      <button class="ghost" @click="live = !live">
        <span class="dot" :class="live ? 'on' : ''"></span>
        Live: <strong>{{ live ? 'ON' : 'PAUSED' }}</strong>
      </button>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>

    <!-- Scope console -->
    <div class="card console">
      <div class="grid scope">
        <label class="fld">Application
          <select v-model="projectId" @change="onProjectChange">
            <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </label>
        <label class="fld">Replica
          <select v-model="replicaSel" @change="refresh">
            <option value="">All replicas (project tail)</option>
            <option v-for="r in replicas" :key="r.id" :value="String(r.replica_index)">
              {{ r.service_name || r.name || 'vm' }} · #{{ r.replica_index }} · {{ r.state }}
            </option>
          </select>
        </label>
        <label class="fld">Search
          <div class="row" style="gap:6px">
            <input v-model="query" placeholder="find the error (replica scope only)…" :disabled="!replicaSel" @keyup.enter="search" />
            <button class="ghost" :disabled="!replicaSel || !query.trim()" @click="search">Run</button>
          </div>
        </label>
      </div>
      <div v-if="!replicaSel" class="muted" style="font-size:12px;margin-top:8px">
        Full-text search runs against one replica's durable log table — select a replica to enable it.
      </div>
    </div>

    <!-- Terminal -->
    <div class="term" v-if="!error">
      <div class="term-bar">
        <span class="dots"><i></i><i></i><i></i></span>
        <span class="mono term-src">
          {{ replicaSel ? `replica #${replicaSel}${searchMode ? ` · search “${lastQuery}”` : ' · durable history'}` : 'project aggregate · hot tail' }}
        </span>
        <span class="term-meta">{{ lines.length }} lines · {{ live ? 'polling 3s' : 'paused' }}</span>
      </div>
      <div class="term-body" ref="termEl">
        <div v-for="(l, i) in lines" :key="i" class="line" :class="level(l.line)">
          <span class="mono ts">{{ l.ts || '' }}</span>
          <span class="mono lvl">{{ level(l.line) }}</span>
          <span class="mono msg">{{ l.line }}</span>
        </div>
        <div v-if="!lines.length" class="empty" style="border:0">
          No log lines yet — output appears here as soon as the replicas write to stdout/stderr.
        </div>
      </div>
      <div class="term-foot">
        <span>auto-scroll {{ autoScroll ? 'pinned' : 'free' }}</span>
        <button class="ghost mini" @click="autoScroll = !autoScroll">Toggle</button>
        <button class="ghost mini" @click="refresh">Refresh now</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, nextTick, onMounted, onUnmounted } from 'vue'
import { get } from '../lib/api.js'

const projects = ref([])
const projectId = ref('')
const replicas = ref([])
const replicaSel = ref('')
const lines = ref([])
const error = ref('')
const query = ref('')
const lastQuery = ref('')
const searchMode = ref(false)
const live = ref(true)
const autoScroll = ref(true)
const termEl = ref(null)

let timer = null

// API returns plain strings (hot ring) or {ID,VMID,Line,TS} rows (durable history).
const norm = (logs) =>
  (logs || []).map((l) => (typeof l === 'string' ? { line: l, ts: '' } : { line: l.Line ?? l.line ?? '', ts: fmtTs(l.TS ?? l.ts) }))

const fmtTs = (t) => {
  if (!t) return ''
  const d = new Date(t)
  return isNaN(d) ? '' : d.toISOString().slice(11, 19)
}

const level = (line) => {
  const s = String(line).toLowerCase()
  if (s.includes('error') || s.includes('fatal') || s.includes('panic')) return 'err'
  if (s.includes('warn')) return 'warn'
  return 'info'
}

async function refresh() {
  if (!projectId.value) return
  error.value = ''
  try {
    let d
    if (searchMode.value) {
      d = await get(`/projects/${projectId.value}/replicas/${replicaSel.value}/logs/search?q=${encodeURIComponent(lastQuery.value)}&tail=300`)
    } else if (replicaSel.value) {
      d = await get(`/projects/${projectId.value}/replicas/${replicaSel.value}/logs?tail=300&history=pg`)
    } else {
      d = await get(`/projects/${projectId.value}/logs`)
    }
    const out = norm(d?.logs)
    // project aggregate returns one big blob; newest last — keep as-is for hot tail,
    // durable history/search return newest-first, flip to chronological.
    lines.value = searchMode.value || replicaSel.value ? out.reverse() : out.slice(-300)
    if (autoScroll.value) {
      await nextTick()
      if (termEl.value) termEl.value.scrollTop = termEl.value.scrollHeight
    }
  } catch (e) {
    error.value = e.message || 'failed to load logs'
  }
}

function search() {
  if (!replicaSel.value || !query.value.trim()) return
  lastQuery.value = query.value.trim()
  searchMode.value = true
  refresh()
}

function onProjectChange() {
  searchMode.value = false
  replicaSel.value = ''
  loadReplicas().then(refresh)
}

async function loadReplicas() {
  replicas.value = []
  try {
    const r = await get(`/projects/${projectId.value}/replicas?limit=200`)
    replicas.value = Array.isArray(r) ? r : []
  } catch (_) { /* replica list optional; project tail still works */ }
}

onMounted(async () => {
  try {
    const d = await get('/projects?limit=200')
    projects.value = Array.isArray(d) ? d : []
    if (projects.value.length) {
      projectId.value = projects.value[0].id
      await loadReplicas()
      await refresh()
    } else {
      error.value = 'No projects yet — create a project to start streaming logs.'
    }
  } catch (e) { error.value = e.message || 'failed to load projects' }
  timer = setInterval(() => { if (live.value && !searchMode.value) refresh() }, 3000)
})

onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ink3); display: inline-block; margin-right: 4px }
.dot.on { background: var(--green); box-shadow: 0 0 6px var(--green) }
.console { margin-top: 16px }
.scope { grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); margin-top: 8px }
.fld { display: flex; flex-direction: column; gap: 4px; font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3) }
.fld input, .fld select { text-transform: none; letter-spacing: 0 }
.term { margin-top: 14px; border-radius: var(--r); overflow: hidden; border: 1px solid #2c2c2e }
.term-bar { display: flex; align-items: center; gap: 10px; padding: 8px 12px; background: #1D1D1F; color: #98989d }
.dots i { width: 10px; height: 10px; border-radius: 50%; display: inline-block; margin-right: 5px; background: #555 }
.dots i:nth-child(1) { background: var(--red) } .dots i:nth-child(2) { background: var(--orange) } .dots i:nth-child(3) { background: var(--green) }
.term-src { flex: 1 }
.term-meta { font-size: 11.5px }
.term-body { background: #111; color: #e8e8ed; padding: 12px; height: 440px; overflow-y: auto; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 12.5px; line-height: 1.6 }
.line { display: flex; gap: 10px; padding: 1px 4px; border-radius: 4px; align-items: baseline }
.line:hover { background: rgba(255, 255, 255, .05) }
.ts { color: #6e6e73; flex: 0 0 64px }
.lvl { flex: 0 0 44px; font-weight: 700 }
.line.info .lvl { color: #30d158 } .line.warn .lvl { color: #ff9f0a } .line.err .lvl { color: #ff453a }
.line.warn { background: rgba(255, 159, 10, .07) } .line.err { background: rgba(255, 69, 58, .1) }
.msg { white-space: pre-wrap; word-break: break-all }
.term-foot { display: flex; align-items: center; gap: 10px; padding: 8px 12px; background: #1D1D1F; color: #98989d; font-size: 12px }
button.ghost.mini { padding: 3px 10px; font-size: 12px; color: #e8e8ed; border-color: #3a3a3c; background: transparent }
.error-bar { margin-top: 12px; padding: 10px 12px; border-radius: var(--r2); background: rgba(255, 69, 58, .09); color: #C22A22; font-size: 13px }
</style>
