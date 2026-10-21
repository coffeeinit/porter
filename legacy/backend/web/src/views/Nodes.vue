<template>
  <div>
    <div class="row" style="align-items:flex-start">
      <div>
        <h1>Nodes &amp; MicroVM Fleet</h1>
        <p class="muted">Registered compute hosts, live capacity, per-node analytics, and daemon logs.</p>
      </div>
      <button style="margin-left:auto" @click="openMint">Mint Enrollment Token</button>
    </div>

    <div v-if="error" class="empty" style="margin-top:14px">Failed to load fleet — {{ error }}</div>

    <div class="statrow" v-else>
      <div class="card stat">
        <div class="slabel">Fleet Nodes</div>
        <div class="snum">{{ servers.length }}</div>
        <div class="muted small">{{ onlineCount }} online · {{ servers.length - onlineCount }} offline/unreported</div>
      </div>
      <div class="card stat">
        <div class="slabel">vCPU Capacity</div>
        <div class="snum">{{ totalVCPUs }}</div>
        <div class="muted small">Reported host threads</div>
      </div>
      <div class="card stat">
        <div class="slabel">Memory</div>
        <div class="snum">{{ fmtGiB(totalMem) }}</div>
        <div class="muted small">Across the fleet</div>
      </div>
      <div class="card stat">
        <div class="slabel">Placed VMs</div>
        <div class="snum">{{ totalVMs }}</div>
        <div class="muted small">Firecracker microVMs</div>
      </div>
    </div>

    <template v-if="!error">
      <h2>Fleet</h2>
      <div class="card" style="padding:0">
        <div v-if="loading" class="empty">Loading nodes…</div>
        <div v-else-if="!servers.length" class="empty">
          No nodes registered yet. Mint an enrollment token below and run the Porter agent on a host to enroll it.
        </div>
        <table v-else>
          <thead>
            <tr>
              <th>Node</th>
              <th>Status</th>
              <th class="num">vCPUs</th>
              <th class="num">RAM</th>
              <th class="num">VMs</th>
              <th>OS / Arch</th>
              <th>Last seen</th>
              <th style="width:70px"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in servers" :key="s.id" class="click" @click="openFull(s.id)">
              <td>
                <router-link :to="'/nodes/' + s.id" style="color:var(--ink)">
                  <div class="pname">{{ s.name }}</div>
                  <div class="mono muted small">{{ s.address || s.id }}</div>
                </router-link>
              </td>
              <td><span class="pill" :class="pillClass(s.status)">{{ s.status || 'unknown' }}</span></td>
              <td class="num">{{ s.vcpus || '—' }}</td>
              <td class="num">{{ s.mem_mib ? fmtGiB(s.mem_mib) : '—' }}</td>
              <td class="num">{{ s.vms }}</td>
              <td class="muted">{{ [s.os, s.arch].filter(Boolean).join(' · ') || '—' }}</td>
              <td class="muted">{{ s.last_seen ? ago(s.last_seen) : 'never' }}</td>
              <td style="text-align:right"><span class="pill info">View</span></td>
            </tr>
          </tbody>
        </table>
        <div class="band" v-if="servers.length">Showing {{ servers.length }} node{{ servers.length === 1 ? '' : 's' }} · click a node for capacity, analytics, VMs, and logs</div>
      </div>

      <h2>Enrollment Tokens</h2>
      <div class="card" style="padding:0">
        <div v-if="tokErr" class="empty">Failed to load tokens — {{ tokErr }}</div>
        <div v-else-if="!tokens.length" class="empty">No enrollment tokens minted yet. Mint one, then run the Porter agent on the host with it — the value is shown exactly once.</div>
        <table v-else>
          <thead><tr><th>Token</th><th>Provider</th><th>Used by</th><th>Expires</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="t in tokens" :key="t.token">
              <td class="mono">{{ t.token }}</td>
              <td><span class="pill info">{{ t.provider }}</span></td>
              <td class="mono">{{ t.used_by || '—' }}</td>
              <td :class="expired(t.expires_at) ? 'muted' : ''">{{ fmtTs(t.expires_at) }}{{ expired(t.expires_at) ? ' (expired)' : '' }}</td>
              <td class="muted">{{ ago(t.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div v-if="showMint" class="overlay" @click.self="showMint = false">
      <form class="card dialog" @submit.prevent="mint">
        <template v-if="!minted">
          <h2 style="margin:0 0 4px">Mint Enrollment Token</h2>
          <p class="muted small" style="margin-bottom:12px">One-time host bootstrap token. The raw value is returned once; the list only keeps a masked form.</p>
          <label class="flabel" for="e-provider">Provider</label>
          <input id="e-provider" v-model="mintForm.provider" placeholder="customer_owned">
          <label class="flabel" for="e-ttl">TTL (hours, max 168)</label>
          <input id="e-ttl" v-model.number="mintForm.ttl_hours" type="number" min="1" max="168">
          <div v-if="mintErr" class="empty" style="margin-top:12px;color:var(--red)">{{ mintErr }}</div>
          <div class="row" style="justify-content:flex-end;margin-top:14px">
            <button type="button" class="ghost" @click="showMint = false">Cancel</button>
            <button type="submit" :disabled="minting">{{ minting ? 'Minting…' : 'Mint Token' }}</button>
          </div>
        </template>
        <template v-else>
          <h2 style="margin:0 0 4px">Enrollment Token Minted</h2>
          <p class="muted small" style="margin-bottom:10px">Copy it now — it expires {{ fmtTs(minted.expires_at) }} and is never shown again.</p>
          <div class="tokbox mono">{{ minted.token }}</div>
          <div v-if="copyState" class="small" style="margin-top:6px;color:var(--ink2)">{{ copyState }}</div>
          <div class="row" style="justify-content:flex-end;margin-top:14px">
            <button type="button" class="ghost" @click="copyToken">Copy</button>
            <button type="button" @click="closeMint">Done</button>
          </div>
        </template>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { get, post, ago, pillClass } from '../lib/api.js'
const router = useRouter()

const servers = ref([])
const loading = ref(true)
const error = ref('')

const selected = ref('')
const detail = ref(null)
const detailErr = ref('')
const vmList = ref([])
const vmErr = ref('')
const logs = ref([])
const logLoading = ref(false)
const logErr = ref('')
const windowMin = ref(60)
const anLoaded = ref(false)
const anErr = ref('')
const cpuPts = ref([])
const memPts = ref([])

const tokens = ref([])
const tokErr = ref('')
const showMint = ref(false)
const minting = ref(false)
const mintErr = ref('')
const minted = ref(null)
const mintForm = ref({ provider: 'customer_owned', ttl_hours: 24 })
const copyState = ref('')

const onlineCount = computed(() => servers.value.filter(s => ['online', 'ready', 'healthy', 'active'].includes(String(s.status).toLowerCase())).length)
const totalVCPUs = computed(() => servers.value.reduce((s, n) => s + (n.vcpus || 0), 0))
const totalMem = computed(() => servers.value.reduce((s, n) => s + (n.mem_mib || 0), 0))
const totalVMs = computed(() => servers.value.reduce((s, n) => s + (n.vms || 0), 0))

const cap = computed(() => (detail.value && detail.value.capacity) || {})
const hb = computed(() => (detail.value && detail.value.heartbeat) || null)
const caps = computed(() => hb.value && hb.value.capabilities ? Object.entries(hb.value.capabilities) : [])
const vmStateEntries = computed(() => {
  const m = (detail.value && detail.value.vm_counts) || {}
  return Object.entries(m).sort((a, b) => b[1] - a[1])
})
const cpuMax = computed(() => Math.max(1, ...cpuPts.value.map(p => p.avg)))
const memMax = computed(() => Math.max(1, ...memPts.value.map(p => p.sum)))

function fmtGiB(mib) {
  if (!mib) return '0 GiB'
  return (mib / 1024).toFixed(mib % 1024 ? 1 : 0) + ' GiB'
}
function pct(v, total) {
  if (!total) return '0%'
  return Math.min(100, Math.round((v / total) * 100)) + '%'
}
function fmtCap(v) {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  return String(v)
}
function barH(v, max) { return Math.max(3, Math.round((v / max) * 100)) + '%' }
function last(pts, key) {
  if (!pts.length) return '0'
  const v = pts[pts.length - 1][key]
  return v != null ? (Math.round(v * 10) / 10) : '0'
}
function tip(pt, unit) {
  const t = new Date(pt.t * 1000).toLocaleTimeString()
  return `${t} — avg ${Math.round(pt.avg * 10) / 10}${unit} · max ${Math.round(pt.max * 10) / 10}${unit} · ${pt.n} samples`
}
function fmtTs(iso) {
  const t = Date.parse(iso)
  return t ? new Date(t).toLocaleString() : '—'
}
function expired(iso) { return Date.parse(iso) < Date.now() }

async function load() {
  loading.value = true
  error.value = ''
  try {
    servers.value = await get('/servers') || []
  } catch (e) { error.value = e.message }
  loading.value = false
}

async function loadTokens() {
  tokErr.value = ''
  try {
    const d = await get('/nodes/enrollment-tokens')
    tokens.value = (d && d.tokens) || []
  } catch (e) { tokErr.value = e.message }
}

function openFull(id) {
  router.push(`/nodes/${id}`)
}

async function select(id) {
  selected.value = selected.value === id ? '' : id
  detail.value = null
  vmList.value = []
  logs.value = []
  cpuPts.value = memPts.value = []
  anLoaded.value = false
  if (!selected.value) return
  detailErr.value = vmErr.value = logErr.value = anErr.value = ''
  try {
    detail.value = await get(`/servers/${id}/detail`)
    if (detail.value.recent_logs && !logs.value.length) logs.value = detail.value.recent_logs
  } catch (e) { detailErr.value = e.message; return }
  loadAnalytics()
  loadVMs(id)
  loadLogs()
}

async function loadAnalytics() {
  const id = selected.value
  if (!id) return
  anErr.value = ''
  anLoaded.value = false
  const step = windowMin.value <= 60 ? 60 : windowMin.value <= 360 ? 300 : 900
  try {
    const d = await get(`/servers/${id}/analytics?minutes=${windowMin.value}&step=${step}`)
    const s = (d && d.series) || {}
    cpuPts.value = s.cpu_percent || []
    memPts.value = s.memory_mib || []
  } catch (e) { anErr.value = e.message }
  anLoaded.value = true
}

async function loadVMs(id) {
  vmErr.value = ''
  try {
    const d = await get(`/servers/${id}/vms?limit=200`)
    vmList.value = (d && d.vms) || []
  } catch (e) { vmErr.value = e.message }
}

async function loadLogs() {
  const id = selected.value
  if (!id) return
  logLoading.value = true
  logErr.value = ''
  try {
    const d = await get(`/servers/${id}/logs?limit=200`)
    logs.value = (d && d.logs) || []
  } catch (e) { logErr.value = e.message }
  logLoading.value = false
}

function openMint() {
  mintErr.value = ''
  minted.value = null
  copyState.value = ''
  showMint.value = true
}

async function mint() {
  minting.value = true
  mintErr.value = ''
  try {
    const ttl = Math.min(168, Math.max(1, mintForm.value.ttl_hours || 24))
    minted.value = await post('/nodes/enrollment-tokens', {
      provider: mintForm.value.provider || 'customer_owned',
      ttl_hours: ttl,
    })
    loadTokens()
  } catch (e) { mintErr.value = e.message }
  minting.value = false
}

async function copyToken() {
  try {
    await navigator.clipboard.writeText(minted.value.token)
    copyState.value = 'Copied to clipboard.'
  } catch (_) { copyState.value = 'Copy failed — select the token text manually.' }
}

function closeMint() {
  showMint.value = false
  minted.value = null
}

onMounted(() => { load(); loadTokens() })
</script>

<style scoped>
.statrow { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-top: 14px; }
.stat .slabel { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3); font-weight: 600; }
.stat .snum { font-size: 26px; font-weight: 700; letter-spacing: -.02em; font-variant-numeric: tabular-nums; margin: 2px 0; }
.small { font-size: 12px; }
.num { text-align: right; }
th.num { text-align: right; }
.pname { font-weight: 600; }
.click { cursor: pointer; }
.click:hover td { background: var(--soft); }
tr.sel td { background: rgba(0,113,227,.06); }
.band { background: rgba(0,113,227,.05); border-top: 1px solid var(--soft); padding: 8px 12px; font-size: 12px; color: var(--ink2); border-radius: 0 0 var(--r) var(--r); }
.slabel { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3); font-weight: 600; }
.capline { margin-top: 10px; font-size: 13px; }
.bar { height: 6px; border-radius: 999px; background: var(--soft); margin-top: 4px; overflow: hidden; }
.fill { height: 100%; border-radius: 999px; background: var(--blue); }
.kv { display: flex; justify-content: space-between; gap: 10px; padding: 3px 0; font-size: 12.5px; }
.kv span:first-child { color: var(--ink2); }
.chart { display: flex; align-items: flex-end; gap: 2px; height: 72px; padding: 6px; background: var(--solid); border: 1px solid var(--hair); border-radius: var(--r2); }
.cbar { flex: 1; min-width: 2px; background: var(--blue); border-radius: 2px 2px 0 0; opacity: .85; }
.cbar.mem { background: var(--purple); }
.logbox { margin-top: 10px; max-height: 260px; overflow-y: auto; background: var(--solid); border: 1px solid var(--hair); border-radius: var(--r2); padding: 8px 10px; }
.logline { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 12px; padding: 2px 0; border-bottom: 1px solid var(--soft); display: flex; gap: 10px; }
.logts { color: var(--ink3); flex-shrink: 0; }
.overlay { position: fixed; inset: 0; background: rgba(0,0,0,.32); display: grid; place-items: center; z-index: 20; }
.dialog { width: 440px; max-width: calc(100vw - 40px); background: #fff; }
.flabel { display: block; font-size: 11.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--ink2); margin: 10px 0 4px; }
.tokbox { background: var(--solid); border: 1px solid var(--hair); border-radius: var(--r2); padding: 12px; word-break: break-all; font-size: 13px; }
</style>
