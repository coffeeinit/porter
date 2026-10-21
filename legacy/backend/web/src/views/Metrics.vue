<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Metrics &amp; Observability</h1>
        <p class="muted">CPU and memory timeseries per replica, plus node-level aggregates from the server analytics API.</p>
      </div>
      <div class="row">
        <select v-model="projectId" style="width:auto" @change="onProjectChange">
          <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
        <select v-model="replicaSel" style="width:auto" @change="loadMetrics">
          <option value="">All replicas</option>
          <option v-for="r in replicas" :key="r.id" :value="String(r.replica_index)">
            {{ r.service_name || r.name || 'vm' }} · #{{ r.replica_index }}
          </option>
        </select>
        <button class="ghost" @click="loadAll">Refresh</button>
      </div>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>
    <template v-else>
      <div class="grid kpis">
        <div class="card kpi">
          <span class="kpi-label">Avg CPU</span>
          <div class="kpi-value">{{ kpi.cpu }} <small>%</small></div>
          <span class="muted">{{ kpi.samples }} samples collected</span>
        </div>
        <div class="card kpi">
          <span class="kpi-label">Avg Memory</span>
          <div class="kpi-value">{{ kpi.mem }} <small>MiB</small></div>
          <span class="muted">across {{ kpi.vms }} replica(s)</span>
        </div>
        <div class="card kpi">
          <span class="kpi-label">Replicas</span>
          <div class="kpi-value">{{ replicas.length }}</div>
          <span class="muted">in {{ currentProjectName }}</span>
        </div>
        <div class="card kpi">
          <span class="kpi-label">Window</span>
          <div class="kpi-value">{{ replicaSel ? 'latest 60' : 'latest 30/VM' }}</div>
          <span class="muted">most recent samples per VM</span>
        </div>
      </div>

      <div class="grid charts">
        <div class="card">
          <h2>CPU Utilization by Replica</h2>
          <p class="muted cap">cpu_percent · most recent {{ replicaSel ? 60 : 30 }} samples per VM</p>
          <svg v-if="cpuSeries.length" viewBox="0 0 540 200" class="chart" preserveAspectRatio="none">
            <line x1="0" x2="540" y1="50" y2="50" class="grid" />
            <line x1="0" x2="540" y1="100" y2="100" class="grid" />
            <line x1="0" x2="540" y1="150" y2="150" class="grid" />
            <polyline v-for="(s, i) in cpuSeries" :key="s.vm" :points="toPoly(s.pts, 100)" :style="{ stroke: color(i) }" class="ln" />
          </svg>
          <div v-else class="empty">No CPU samples yet — the collector writes metrics once replicas are running.</div>
          <div class="legend" v-if="cpuSeries.length">
            <span v-for="(s, i) in cpuSeries" :key="s.vm" class="lg">
              <i :style="{ background: color(i) }"></i>{{ s.label }}
            </span>
          </div>
        </div>

        <div class="card">
          <h2>Memory by Replica</h2>
          <p class="muted cap">memory_mib · latest sample per VM</p>
          <svg v-if="memBars.length" viewBox="0 0 540 200" class="chart" preserveAspectRatio="none">
            <line x1="0" x2="540" y1="50" y2="50" class="grid" />
            <line x1="0" x2="540" y1="100" y2="100" class="grid" />
            <line x1="0" x2="540" y1="150" y2="150" class="grid" />
            <rect v-for="b in memBars" :key="b.vm" :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="2" class="bar-r" />
          </svg>
          <div v-else class="empty">No memory samples yet.</div>
          <div class="axis" v-if="memBars.length"><span>0</span><span>{{ memMax }} MiB</span></div>
        </div>

        <div class="card">
          <h2>Node CPU Timeseries</h2>
          <p class="muted cap">GET /servers/{id}/analytics · cpu_percent avg/max across placed VMs (last 60 min)</p>
          <select v-model="serverId" style="width:auto;margin-bottom:8px" @change="loadServerAnalytics">
            <option value="">{{ servers.length ? 'Select server…' : 'No servers registered' }}</option>
            <option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name || s.id }} ({{ s.vms }} VMs)</option>
          </select>
          <svg v-if="serverCpu.length" viewBox="0 0 540 200" class="chart" preserveAspectRatio="none">
            <line x1="0" x2="540" y1="50" y2="50" class="grid" />
            <line x1="0" x2="540" y1="100" y2="100" class="grid" />
            <line x1="0" x2="540" y1="150" y2="150" class="grid" />
            <polygon :points="band(serverCpu, 100)" class="band" />
            <polyline :points="toPoly(serverCpu.map((p) => p.avg), 100)" class="ln main" />
          </svg>
          <div v-else class="empty">Pick a registered server to render its bucketed CPU timeseries.</div>
        </div>

        <div class="card">
          <h2>Node Memory Timeseries</h2>
          <p class="muted cap">memory_mib summed across the server's placed VMs</p>
          <template v-if="serverId">
            <svg v-if="serverMem.length" viewBox="0 0 540 200" class="chart" preserveAspectRatio="none">
              <line x1="0" x2="540" y1="50" y2="50" class="grid" />
              <line x1="0" x2="540" y1="100" y2="100" class="grid" />
              <line x1="0" x2="540" y1="150" y2="150" class="grid" />
              <polygon :points="areaPoly(serverMem.map((p) => p.sum), serverMemMax)" class="band" />
              <polyline :points="toPoly(serverMem.map((p) => p.sum), serverMemMax)" class="ln main" />
            </svg>
            <div class="axis" v-if="serverMem.length"><span>0</span><span>{{ Math.round(serverMemMax) }} MiB</span></div>
            <div v-else class="empty">No memory buckets in the last hour for this server.</div>
          </template>
          <div v-else class="empty">Select a server above.</div>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get } from '../lib/api.js'

const projects = ref([])
const projectId = ref('')
const replicas = ref([])
const replicaSel = ref('')
const samples = ref([])
const servers = ref([])
const serverId = ref('')
const serverSeries = ref({})
const error = ref('')

const COLORS = ['#0071E3', '#30D158', '#BF5AF2', '#FF9F0A', '#FF453A', '#5AC8FA', '#8E8E93']
const color = (i) => COLORS[i % COLORS.length]
const currentProjectName = computed(() => projects.value.find((p) => p.id === projectId.value)?.name || '—')

// ---- SVG helpers (viewBox 540x200, y inverted, fixed scale arg or auto) ----
const toPoly = (vals, max) => {
  const pts = norm(vals, max)
  return pts.map((y, i) => `${(i / Math.max(1, pts.length - 1)) * 540},${y}`).join(' ')
}
const norm = (vals, max) => {
  const m = max ?? Math.max(...vals, 1) * 1.1
  return vals.map((v) => 190 - Math.min(1, Math.max(0, v / m)) * 180)
}
const band = (pts, max) => {
  const m = max ?? Math.max(...pts.map((p) => p.max), 1) * 1.1
  const top = pts.map((p, i) => `${(i / Math.max(1, pts.length - 1)) * 540},${190 - Math.min(1, p.max / m) * 180}`)
  const bottom = pts.map((p, i) => `${(i / Math.max(1, pts.length - 1)) * 540},${190 - Math.min(1, p.avg / m) * 180}`).reverse()
  return `${top.join(' ')} ${bottom.join(' ')}`
}
const areaPoly = (vals, max) => `${toPoly(vals, max)} 540,190 0,190`

// ---- Data shaping ----
const groupByVm = (metric) => {
  const by = {}
  for (const s of samples.value) {
    if (s.metric !== metric || !s.vm_id) continue
    ;(by[s.vm_id] ||= { vm: s.vm_id, pts: [] })
    by[s.vm_id].pts.push({ v: s.value, ts: new Date(s.ts).getTime() || 0 })
  }
  return Object.values(by)
    .map((g) => ({
      vm: g.vm,
      label: labelFor(g.vm),
      pts: g.pts.sort((a, b) => a.ts - b.ts).slice(-60).map((p) => p.v),
    }))
    .sort((a, b) => a.label.localeCompare(b.label))
}

const labelFor = (vmId) => {
  const r = replicas.value.find((x) => x.id === vmId)
  return r ? `${r.service_name || r.name || 'vm'} #${r.replica_index}` : vmId.slice(0, 8)
}

const cpuSeries = computed(() => groupByVm('cpu_percent'))
const memBars = computed(() => {
  const g = groupByVm('memory_mib').map((s) => ({ vm: s.vm, v: s.pts[s.pts.length - 1] || 0 }))
  if (!g.length) return []
  const m = Math.max(...g.map((x) => x.v), 1)
  return g.map((x, i) => ({
    vm: x.vm,
    x: 20 + i * (500 / g.length),
    w: Math.max(6, 500 / g.length - 14),
    y: 190 - (x.v / m) * 170,
    h: (x.v / m) * 170,
  }))
})
const memMax = computed(() => Math.round(Math.max(...groupByVm('memory_mib').map((s) => s.pts.at(-1) || 0), 1)))
const kpi = computed(() => {
  const cpu = samples.value.filter((s) => s.metric === 'cpu_percent')
  const mem = samples.value.filter((s) => s.metric === 'memory_mib')
  const avg = (a) => (a.length ? (a.reduce((s, x) => s + x.value, 0) / a.length).toFixed(1) : '—')
  return {
    cpu: avg(cpu),
    mem: avg(mem),
    samples: samples.value.length,
    vms: new Set(samples.value.map((s) => s.vm_id)).size,
  }
})

const serverCpu = computed(() => serverSeries.value.cpu_percent || [])
const serverMem = computed(() => serverSeries.value.memory_mib || [])
const serverMemMax = computed(() => Math.max(...serverMem.value.map((p) => p.sum), 1) * 1.1)

// ---- Loads ----
async function loadMetrics() {
  error.value = ''
  try {
    const d = replicaSel.value
      ? await get(`/projects/${projectId.value}/replicas/${replicaSel.value}/metrics`)
      : await get(`/projects/${projectId.value}/metrics`)
    samples.value = Array.isArray(d) ? d : []
  } catch (e) {
    samples.value = []
    error.value = e.message || 'failed to load metrics'
  }
}

async function loadServerAnalytics() {
  serverSeries.value = {}
  if (!serverId.value) return
  try {
    const d = await get(`/servers/${serverId.value}/analytics?minutes=60&step=60`)
    serverSeries.value = d?.series || {}
  } catch (e) {
    error.value = e.message || 'failed to load server analytics'
  }
}

async function onProjectChange() {
  replicaSel.value = ''
  try {
    const r = await get(`/projects/${projectId.value}/replicas?limit=200`)
    replicas.value = Array.isArray(r) ? r : []
  } catch (_) { replicas.value = [] }
  await loadMetrics()
}

async function loadAll() {
  await onProjectChange()
  await loadServerAnalytics()
}

onMounted(async () => {
  try {
    const [d, sv] = await Promise.all([
      get('/projects?limit=200'),
      get('/servers?limit=200').catch(() => []),
    ])
    projects.value = Array.isArray(d) ? d : []
    servers.value = Array.isArray(sv) ? sv : sv?.servers || []
    if (projects.value.length) {
      projectId.value = projects.value[0].id
      await onProjectChange()
    } else {
      error.value = 'No projects yet — metrics are collected per running replica.'
    }
  } catch (e) { error.value = e.message || 'failed to load metrics scope' }
})
</script>

<style scoped>
.kpis { grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); margin-top: 16px }
.kpi { display: flex; flex-direction: column; gap: 4px }
.kpi-label { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3) }
.kpi-value { font-size: 24px; font-weight: 600; letter-spacing: -.02em }
.kpi-value small { font-size: 13px; font-weight: 400; color: var(--ink2) }
.charts { grid-template-columns: repeat(auto-fit, minmax(420px, 1fr)); margin-top: 14px }
.cap { font-size: 12px; margin-bottom: 10px }
.chart { width: 100%; height: 200px; background: var(--solid); border-radius: var(--r2) }
.grid { stroke: var(--hair); stroke-dasharray: 3 3 }
.ln { fill: none; stroke-width: 2; stroke-linejoin: round; stroke-linecap: round }
.ln.main { stroke: var(--blue) }
.band { fill: rgba(0, 113, 227, .12); stroke: none }
.bar-r { fill: var(--blue) }
.legend { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 8px; font-size: 12px; color: var(--ink2) }
.legend .lg { display: inline-flex; align-items: center; gap: 5px }
.legend i { width: 10px; height: 3px; border-radius: 2px; display: inline-block }
.axis { display: flex; justify-content: space-between; font-size: 11px; color: var(--ink3); margin-top: 4px }
.error-bar { margin-top: 12px; padding: 10px 12px; border-radius: var(--r2); background: rgba(255, 69, 58, .09); color: #C22A22; font-size: 13px }
h2 { margin: 0 }
</style>
