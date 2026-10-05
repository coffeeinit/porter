<template>
  <div>
    <div class="row" style="align-items:flex-start">
      <div>
        <h1>Networking &amp; Gateways</h1>
        <p class="muted">Per-project virtual networks, firewall rules, and edge ingress traffic.</p>
      </div>
      <div style="margin-left:auto;min-width:220px">
        <label class="flabel" for="n-project" style="margin-top:0">Project</label>
        <select id="n-project" v-model="project" :disabled="!projects.length">
          <option value="" disabled>No projects available</option>
          <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
      </div>
    </div>

    <div v-if="loadErr" class="empty" style="margin-top:14px">Failed to load — {{ loadErr }}</div>

    <div class="statrow" v-else>
      <div class="card stat">
        <div class="slabel">Virtual Networks</div>
        <div class="snum">{{ networks.length }}</div>
        <div class="muted small">Isolated bridge subnets</div>
      </div>
      <div class="card stat">
        <div class="slabel">Firewall Rules</div>
        <div class="snum">{{ rules.length }}</div>
        <div class="muted small">{{ activeRules }} active</div>
      </div>
      <div class="card stat">
        <div class="slabel">Ingress Entries</div>
        <div class="snum">{{ traffic.length }}</div>
        <div class="muted small">Fleet-wide edge traffic ring</div>
      </div>
      <div class="card stat">
        <div class="slabel">2xx Share</div>
        <div class="snum">{{ okShare }}</div>
        <div class="muted small">Of sampled requests</div>
      </div>
    </div>

    <h2>Virtual Networks <span class="muted small" style="font-weight:400">— {{ projectName || 'select a project' }}</span></h2>
    <div class="card" style="padding:0">
      <div v-if="netErr" class="empty">Failed to load networks — {{ netErr }}</div>
      <div v-else-if="netLoading" class="empty">Loading networks…</div>
      <div v-else-if="!project" class="empty">Select a project above to list its networks.</div>
      <div v-else-if="!networks.length" class="empty">No networks created for this project yet.</div>
      <table v-else>
        <thead><tr><th>Name</th><th>CIDR</th><th>Driver</th><th>Created</th></tr></thead>
        <tbody>
          <tr v-for="n in networks" :key="n.id">
            <td class="mono" style="font-weight:600">{{ n.name }}</td>
            <td class="mono">{{ n.cidr }}</td>
            <td><span class="pill info">{{ n.driver }}</span></td>
            <td class="muted">{{ ago(n.created_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <h2>Firewall Rules <span class="muted small" style="font-weight:400">— {{ projectName || 'select a project' }}</span></h2>
    <div class="card" style="padding:0">
      <div v-if="fwErr" class="empty">Failed to load firewall rules — {{ fwErr }}</div>
      <div v-else-if="fwLoading" class="empty">Loading rules…</div>
      <div v-else-if="!project" class="empty">Select a project above to list its firewall rules.</div>
      <div v-else-if="!rules.length" class="empty">No firewall rules for this project yet.</div>
      <table v-else>
        <thead><tr><th>Action</th><th>Direction</th><th>Ports / Proto</th><th>Source</th><th class="num">Priority</th><th>State</th></tr></thead>
        <tbody>
          <tr v-for="r in rules" :key="r.id">
            <td><span class="pill" :class="String(r.action).toLowerCase() === 'allow' ? 'ok' : 'bad'">{{ r.action }}</span></td>
            <td>{{ r.direction }}</td>
            <td class="mono">{{ r.ports || 'any' }} / {{ r.proto || 'all' }}</td>
            <td class="mono">{{ r.source || '0.0.0.0/0' }}</td>
            <td class="num">{{ r.priority }}</td>
            <td><span class="pill" :class="r.active ? 'ok' : 'info'">{{ r.active ? 'active' : 'inactive' }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>

    <h2>Edge Gateways &amp; Tunnels</h2>
    <div class="card">
      <div class="row" style="align-items:flex-start;gap:14px">
        <span class="pill info" style="flex-shrink:0">cloudflare</span>
        <p class="muted small" style="margin:0">
          Cloudflare tunnel and ingress management is create-only in the current API
          (<span class="mono">POST /projects/&#123;id&#125;/cf/tunnels</span>) — there is no list endpoint yet, so existing
          tunnels are not available here. Firewall and network state above is live.
        </p>
      </div>
    </div>

    <h2>Ingress Traffic <span class="muted small" style="font-weight:400">— fleet-wide, newest first</span></h2>
    <div class="card" style="padding:0">
      <div v-if="trErr" class="empty">Failed to load traffic — {{ trErr }}</div>
      <div v-else-if="trLoading" class="empty">Loading traffic…</div>
      <div v-else-if="!traffic.length" class="empty">No traffic recorded yet. Requests appear here once the edge gateway routes them.</div>
      <table v-else>
        <thead><tr><th>Time</th><th>Method</th><th>Host</th><th>Path</th><th>Status</th><th class="num">ms</th><th class="num">Bytes out</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in traffic.slice(0, 50)" :key="i">
            <td class="muted">{{ ago(t.timestamp) }}</td>
            <td><span class="pill info">{{ t.method }}</span></td>
            <td class="mono">{{ t.host }}</td>
            <td class="mono">{{ t.path }}</td>
            <td><span class="pill" :class="t.status < 400 ? 'ok' : (t.status < 500 ? 'warn' : 'bad')">{{ t.status }}</span></td>
            <td class="num">{{ t.duration_ms }}</td>
            <td class="num">{{ fmtBytes(t.bytes_out) }}</td>
          </tr>
        </tbody>
      </table>
      <div class="band" v-if="traffic.length">Showing {{ Math.min(50, traffic.length) }} of {{ traffic.length }} entries</div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { get, ago } from '../lib/api.js'

const projects = ref([])
const project = ref('')
const loadErr = ref('')

const networks = ref([])
const netLoading = ref(false)
const netErr = ref('')
const rules = ref([])
const fwLoading = ref(false)
const fwErr = ref('')
const traffic = ref([])
const trLoading = ref(false)
const trErr = ref('')

const projectName = computed(() => (projects.value.find(p => p.id === project.value) || {}).name || '')
const activeRules = computed(() => rules.value.filter(r => r.active).length)
const okShare = computed(() => {
  if (!traffic.value.length) return '—'
  const ok = traffic.value.filter(t => t.status >= 200 && t.status < 300).length
  return Math.round((ok / traffic.value.length) * 100) + '%'
})

function fmtBytes(n) {
  if (!n) return '0'
  if (n < 1024) return String(n)
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1048576).toFixed(1) + ' MB'
}

async function loadMeta() {
  loadErr.value = ''
  try {
    const p = await get('/projects')
    projects.value = Array.isArray(p) ? p : (p.projects || [])
    if (projects.value.length && !project.value) project.value = projects.value[0].id
  } catch (e) { loadErr.value = e.message }
}

async function loadTraffic() {
  trLoading.value = true
  trErr.value = ''
  try {
    const d = await get('/traffic')
    traffic.value = Array.isArray(d) ? d : []
  } catch (e) { trErr.value = e.message }
  trLoading.value = false
}

async function loadProjectNet() {
  if (!project.value) { networks.value = []; rules.value = []; return }
  netLoading.value = fwLoading.value = true
  netErr.value = fwErr.value = ''
  try { networks.value = await get(`/projects/${project.value}/networks`) || [] }
  catch (e) { netErr.value = e.message }
  try { rules.value = await get(`/projects/${project.value}/firewall/rules`) || [] }
  catch (e) { fwErr.value = e.message }
  netLoading.value = fwLoading.value = false
}

watch(project, loadProjectNet)

onMounted(() => { loadMeta(); loadTraffic() })
</script>

<style scoped>
.statrow { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-top: 14px; }
.stat .slabel { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3); font-weight: 600; }
.stat .snum { font-size: 26px; font-weight: 700; letter-spacing: -.02em; font-variant-numeric: tabular-nums; margin: 2px 0; }
.small { font-size: 12px; }
.num { text-align: right; }
th.num { text-align: right; }
.band { background: rgba(0,113,227,.05); border-top: 1px solid var(--soft); padding: 8px 12px; font-size: 12px; color: var(--ink2); border-radius: 0 0 var(--r) var(--r); }
.flabel { display: block; font-size: 11.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--ink2); margin: 10px 0 4px; }
</style>
