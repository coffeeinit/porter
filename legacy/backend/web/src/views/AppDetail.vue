<template>
  <div class="detail">
    <nav class="crumbs">
      <router-link to="/apps">Applications</router-link>
      <span class="sep">/</span>
      <span class="here">{{ project.name || '…' }}</span>
    </nav>

    <div v-if="err" class="errbar">{{ err }} <button class="ghost" @click="load">Retry</button></div>

    <!-- Header -->
    <div class="card head" v-if="project.id">
      <span class="avatar">{{ initials(project.name) }}</span>
      <div class="head-main">
        <div class="row" style="gap:10px;flex-wrap:wrap">
          <h1>{{ project.name }}</h1>
          <span class="pill" :class="pillClass(status)">{{ status }}</span>
          <span v-if="isCompose" class="pill info">compose</span>
        </div>
        <p class="mono dim src">{{ project.image || project.source || 'no image' }}</p>
      </div>
      <div class="head-stats">
        <div class="hstat"><span class="n">{{ vms.length }}</span><span class="l">replicas</span></div>
        <div class="hstat"><span class="n">{{ deployments.length }}</span><span class="l">deployments</span></div>
        <div class="hstat"><span class="n">{{ healthy }}</span><span class="l">healthy</span></div>
      </div>
    </div>

    <div v-if="loading" class="empty">Loading application…</div>

    <!-- Tabs -->
    <template v-else-if="project.id">
      <div class="tabs">
        <button v-for="t in tabs" :key="t" :class="{ active: tab === t }" @click="tab = t">{{ t }}</button>
      </div>

      <!-- Overview -->
      <section v-if="tab === 'Overview'" class="body">
        <h2>MicroVM replicas</h2>
        <div class="card">
          <table v-if="vms.length">
            <thead><tr><th>VM</th><th>State</th><th>Health</th><th>IP</th><th>vCPU</th><th>Memory</th><th>Started</th></tr></thead>
            <tbody>
              <tr v-for="vm in vms" :key="vm.id">
                <td class="mono">{{ shortId(vm.id) }}</td>
                <td><span class="pill" :class="pillClass(vm.state)">{{ vm.state || '—' }}</span></td>
                <td><span class="pill" :class="pillClass(vm.health_status)">{{ vm.health_status || '—' }}</span></td>
                <td class="mono">{{ vm.ip_address || '—' }}</td>
                <td class="tnum">{{ vm.vcpus ?? '—' }}</td>
                <td class="tnum">{{ vm.mem_mib ? `${vm.mem_mib} MiB` : '—' }}</td>
                <td class="muted">{{ vm.started_at ? ago(vm.started_at) : '—' }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="empty">No replicas are running for this application yet.</div>
        </div>

        <h2>Metrics summary</h2>
        <div class="card" v-if="metricRows.length">
          <table>
            <thead><tr><th>Metric</th><th>Latest</th><th>Samples</th><th>Last point</th></tr></thead>
            <tbody>
              <tr v-for="m in metricRows" :key="m.name">
                <td class="mono">{{ m.name }}</td>
                <td class="tnum">{{ m.last }}</td>
                <td class="tnum">{{ m.n }}</td>
                <td class="muted">{{ ago(m.ts) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">Metrics are not available yet — samples appear once replicas report telemetry.</div>

        <h2>Recent activity</h2>
        <div class="card" v-if="events.length">
          <table>
            <thead><tr><th>Status</th><th>Detail</th><th>VM</th><th style="text-align:right">When</th></tr></thead>
            <tbody>
              <tr v-for="e in events" :key="e.id">
                <td><span class="pill" :class="pillClass(e.status)">{{ e.status || 'info' }}</span></td>
                <td>{{ e.detail || '—' }}</td>
                <td class="mono">{{ e.vm_id || '—' }}</td>
                <td class="muted" style="text-align:right">{{ ago(e.ts) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">No health events recorded for this application yet.</div>
      </section>

      <!-- Deployments -->
      <section v-else-if="tab === 'Deployments'" class="body">
        <div class="card" v-if="deployments.length">
          <table>
            <thead><tr><th>Rev</th><th>Label</th><th>Environment</th><th>Status</th><th>Traffic</th><th>Preview</th><th>Created</th></tr></thead>
            <tbody>
              <tr v-for="d in deployments" :key="d.id" class="click-row" @click="$router.push('/deployments')">
                <td class="tnum">#{{ d.revision }}</td>
                <td class="mono">{{ d.version_label || shortId(d.id) }}</td>
                <td>{{ d.environment || '—' }}</td>
                <td><span class="pill" :class="pillClass(d.build_status)">{{ d.build_status || '—' }}</span></td>
                <td class="tnum">{{ d.route_weight ?? 0 }}%</td>
                <td class="mono dim">{{ d.preview_url || '—' }}</td>
                <td class="muted">{{ ago(d.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">No deployments yet for this application.</div>
      </section>

      <!-- Logs -->
      <section v-else-if="tab === 'Logs'" class="body">
        <div class="card log-card" v-if="logs.length">
          <div v-for="(l, i) in logs" :key="i" class="log-line mono">{{ l }}</div>
        </div>
        <div v-else class="empty">No logs available yet — lines appear once replicas emit output.</div>
      </section>

      <!-- Metrics -->
      <section v-else-if="tab === 'Metrics'" class="body">
        <div class="card" v-if="metrics.length">
          <table class="kv">
            <tbody>
              <tr v-for="m in latestMetrics" :key="m.name">
                <th>{{ m.name }}</th>
                <td class="tnum">{{ m.value }} <span class="muted">({{ m.samples }} samples, latest {{ ago(m.ts) }})</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">No metric samples yet — collectors report once replicas are running.</div>
      </section>

      <!-- Events -->
      <section v-else-if="tab === 'Events'" class="body">
        <div class="card" v-if="events.length">
          <table class="kv">
            <tbody>
              <tr v-for="e in events" :key="e.id ?? e.ID ?? e.at">
                <th>{{ fmtDate(e.at || e.timestamp) }}</th>
                <td><span class="pill info">{{ e.name || e.type || e.kind || 'event' }}</span> {{ e.detail || e.message || '' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">No events recorded for this application yet.</div>
      </section>

      <!-- Settings -->
      <section v-else class="body">
        <h2>Application settings</h2>
        <div class="card">
          <table class="kv">
            <tbody>
              <tr><th>Name</th><td>{{ project.name }}</td></tr>
              <tr><th>ID</th><td class="mono">{{ project.id }}</td></tr>
              <tr><th>Image</th><td class="mono">{{ project.image || '—' }}</td></tr>
              <tr><th>Source</th><td>{{ project.source || '—' }}</td></tr>
              <tr><th>Network</th><td class="mono">{{ project.network || '—' }}</td></tr>
              <tr><th>Desired replicas</th><td class="tnum">{{ project.replicas_desired || project.replicas || '—' }}</td></tr>
              <tr><th>Restart policy</th><td>{{ project.restart_policy || '—' }}</td></tr>
              <tr><th>SSH</th><td>{{ project.ssh_enabled ? 'Enabled' : 'Disabled' }}</td></tr>
              <tr><th>Tags</th><td>{{ (project.tags || []).join(', ') || '—' }}</td></tr>
              <tr><th>Created</th><td>{{ fmtDate(project.created_at) }}</td></tr>
            </tbody>
          </table>
        </div>
        <p class="muted note">Editing settings is not available in this view yet — manage configuration from Projects.</p>
      </section>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { get, ago, pillClass } from '../lib/api.js'

const route = useRoute()
const loading = ref(true)
const err = ref('')
const project = ref({})
const vms = ref([])
const deployments = ref([])
const logs = ref([])
const metrics = ref([])
const events = ref([])
const tab = ref('Overview')
const tabs = ['Overview', 'Deployments', 'Logs', 'Metrics', 'Events', 'Settings']

// Latest value per metric name for the Metrics tab.
const latestMetrics = computed(() => {
  const byName = new Map()
  for (const m of metrics.value) {
    const name = m.metric || m.name || 'sample'
    const ts = m.ts || m.timestamp || ''
    const prev = byName.get(name)
    if (!prev || String(ts) > String(prev.ts)) byName.set(name, { name, value: Number(m.value ?? m.avg ?? 0).toFixed(2), ts, samples: (prev?.samples || 0) + 1 })
    else prev.samples += 1
  }
  return [...byName.values()]
})

const status = computed(() => {
  if (!vms.value.length) return 'pending'
  if (vms.value.some((v) => v.state === 'failed' || v.state === 'crashed')) return 'failed'
  if (vms.value.every((v) => v.state === 'running')) return 'running'
  if (vms.value.some((v) => v.state === 'running')) return 'degraded'
  return 'pending'
})
const isCompose = computed(() => project.value.source === 'compose')
const healthy = computed(() => vms.value.filter((v) => v.health_status === 'healthy').length)

// Collapse the flat sample stream into one latest-value row per metric name.
const metricRows = computed(() => {
  const byName = new Map()
  for (const s of metrics.value) {
    if (!s || !s.metric) continue
    const prev = byName.get(s.metric)
    if (!prev || Date.parse(s.ts) > Date.parse(prev.ts)) byName.set(s.metric, s)
  }
  const counts = new Map()
  for (const s of metrics.value) counts.set(s.metric, (counts.get(s.metric) || 0) + 1)
  return [...byName.entries()].map(([name, s]) => ({
    name,
    last: Number.isFinite(s.value) ? Math.round(s.value * 100) / 100 : '—',
    n: counts.get(name),
    ts: s.ts,
  }))
})

function shortId(id) { return String(id || '').slice(0, 8) || '—' }
function initials(name) { return String(name || '?').replace(/[^a-zA-Z0-9]/g, '').slice(0, 2).toUpperCase() || '?' }
function fmtDate(iso) { const t = Date.parse(iso); return t ? new Date(t).toLocaleString() : '—' }

async function load() {
  const id = route.params.id
  if (!id) return
  loading.value = true
  err.value = ''
  const failed = []
  await Promise.all([
    get(`/projects/${encodeURIComponent(id)}?expand=vms`)
      .then((d) => { project.value = d?.project || d || {}; vms.value = d?.vms || [] })
      .catch(() => failed.push('application')),
    get(`/projects/${encodeURIComponent(id)}/deployments?limit=200`)
      .then((d) => { deployments.value = Array.isArray(d) ? d : [] })
      .catch(() => failed.push('deployments')),
    get(`/projects/${encodeURIComponent(id)}/logs`)
      .then((d) => { logs.value = (d && Array.isArray(d.logs)) ? d.logs : [] })
      .catch(() => failed.push('logs')),
    get(`/projects/${encodeURIComponent(id)}/metrics`)
      .then((d) => { metrics.value = Array.isArray(d) ? d : [] })
      .catch(() => failed.push('metrics')),
    get(`/projects/${encodeURIComponent(id)}/events`)
      .then((d) => { events.value = Array.isArray(d) ? d : [] })
      .catch(() => failed.push('events')),
  ])
  if (failed.length) err.value = `Failed to load: ${failed.join(', ')}.`
  loading.value = false
}

onMounted(load)
watch(() => route.params.id, load)
</script>

<style scoped>
.crumbs{display:flex;align-items:center;gap:8px;font-size:11.5px;color:var(--ink2);margin-bottom:12px}
.crumbs .here{color:var(--ink);font-weight:550}
.sep{color:var(--ink3)}
.head{display:flex;align-items:center;gap:16px;padding:20px 24px;margin-bottom:16px;flex-wrap:wrap}
.head h1{margin:0}
.head-main{min-width:220px}
.src{margin-top:3px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:420px}
.dim{color:var(--ink3)}
.avatar{width:44px;height:44px;border-radius:12px;background:linear-gradient(160deg,#777,#333);color:#fff;display:grid;place-items:center;font-size:15px;font-weight:650;flex:none}
.head-stats{display:flex;gap:22px;margin-left:auto}
.hstat{display:flex;flex-direction:column;align-items:flex-end}
.hstat .n{font-size:20px;font-weight:650;font-variant-numeric:tabular-nums}
.hstat .l{font-size:10.5px;color:var(--ink2);text-transform:uppercase;letter-spacing:.04em}
.tabs{display:flex;gap:2px;border-bottom:1px solid var(--hair);margin-bottom:16px}
.tabs button{background:transparent;color:var(--ink2);padding:8px 14px;border-radius:8px 8px 0 0;font-size:13px;font-weight:500;border-bottom:2px solid transparent;margin-bottom:-1px}
.tabs button.active{color:var(--ink);font-weight:600;border-bottom-color:var(--blue)}
.body h2{font-size:13.5px;font-weight:650;margin:18px 0 8px}
.body h2:first-child{margin-top:0}
.click-row{cursor:pointer}
.click-row:hover td{background:rgba(0,0,0,.018)}
.tnum{font-variant-numeric:tabular-nums}
.kv th{width:180px;border-bottom:1px solid var(--soft)}
.kv td{border-bottom:1px solid var(--soft)}
.log-card{background:#161618;color:#E8E8ED;border-color:#161618;padding:14px 16px;max-height:420px;overflow:auto}
.log-line{font-size:12px;line-height:1.6;white-space:pre-wrap;word-break:break-word}
.note{font-size:11.5px;margin-top:10px}
.errbar{display:flex;align-items:center;gap:10px;background:rgba(255,69,58,.08);border:1px solid rgba(255,69,58,.25);color:#C22A22;border-radius:var(--r2);padding:9px 12px;font-size:12.5px;margin-bottom:14px}
.errbar button{padding:4px 10px;font-size:12px}
</style>
