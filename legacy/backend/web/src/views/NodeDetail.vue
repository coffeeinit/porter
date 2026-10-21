<template>
  <div v-if="d">
    <div class="row" style="justify-content:space-between;margin-bottom:4px">
      <h1 style="margin:0">{{ d.server?.hostname || id }}</h1>
      <span class="pill" :class="d.server?.status === 'ready' ? 'ok' : 'warn'">{{ d.server?.status || 'unknown' }}</span>
    </div>
    <p class="muted" style="margin-bottom:14px">
      {{ d.server?.address }} · {{ d.server?.os || '—' }} {{ d.server?.arch || '' }} · agent {{ d.heartbeat?.agent_version || '—' }} · last seen {{ lastSeen(d.server?.last_seen) }}
    </p>

    <div class="grid" style="grid-template-columns:repeat(auto-fit,minmax(170px,1fr));margin-bottom:14px">
      <div class="card"><div class="muted">vCPU total / allocated</div><div style="font-size:22px;font-weight:700">{{ d.capacity?.vcpu_total ?? '—' }} / {{ d.capacity?.vcpu_allocated ?? '—' }}</div></div>
      <div class="card"><div class="muted">RAM total / allocated</div><div style="font-size:22px;font-weight:700">{{ fmtMiB(d.capacity?.mem_total_mib) }} / {{ fmtMiB(d.capacity?.mem_allocated_mib) }}</div></div>
      <div class="card"><div class="muted">Free (agent-reported)</div><div style="font-size:22px;font-weight:700">{{ d.heartbeat?.vcpu_free ?? '—' }} vCPU · {{ fmtMiB(d.heartbeat?.mem_free_mib) }}</div></div>
      <div class="card"><div class="muted">VMs on node</div><div style="font-size:22px;font-weight:700">{{ d.vm_counts ? Object.values(d.vm_counts).reduce((a, b) => a + b, 0) : '—' }}</div></div>
    </div>

    <div class="row" style="justify-content:space-between">
      <h2 style="margin:0">Resource graphs</h2>
      <div class="row">
        <button v-for="w in windows" :key="w.m" class="ghost" :class="{ on: win === w.m }" @click="win = w.m; loadSeries()">{{ w.label }}</button>
      </div>
    </div>
    <div class="grid" style="grid-template-columns:1fr 1fr;margin:10px 0 14px">
      <div class="card">
        <div class="row" style="justify-content:space-between"><b>CPU %</b><span class="muted">peak {{ peak('cpu_percent') }}%</span></div>
        <svg :viewBox="'0 0 400 120'" style="width:100%;height:120px">
          <polyline :points="line('cpu_percent', 100)" fill="none" stroke="#0071E3" stroke-width="2"/>
          <text x="4" y="12" font-size="10" fill="#8E8E93">100</text>
        </svg>
        <div class="muted" style="font-size:11px">{{ seriesLabel('cpu_percent') }}</div>
      </div>
      <div class="card">
        <div class="row" style="justify-content:space-between"><b>RAM (MiB)</b><span class="muted">peak {{ peak('memory_mib') }} MiB</span></div>
        <svg :viewBox="'0 0 400 120'" style="width:100%;height:120px">
          <polyline :points="line('memory_mib', yMax('memory_mib'))" fill="none" stroke="#30D158" stroke-width="2"/>
          <text x="4" y="12" font-size="10" fill="#8E8E93">{{ yMax('memory_mib') }}</text>
        </svg>
        <div class="muted" style="font-size:11px">{{ seriesLabel('memory_mib') }}</div>
      </div>
    </div>

    <h2>Placed VMs</h2>
    <div class="card">
      <table v-if="d.vms?.length">
        <thead><tr><th>VM</th><th>Project</th><th>State</th><th>Health</th><th>IP</th><th>vCPU/RAM</th></tr></thead>
        <tbody>
          <tr v-for="v in d.vms" :key="v.id">
            <td class="mono">{{ v.name }}</td>
            <td class="mono muted">{{ v.project_name || v.project_id }}</td>
            <td><span class="pill" :class="pillClass(v.state)">{{ v.state }}</span></td>
            <td><span class="pill" :class="pillClass(v.health_status)">{{ v.health_status || '—' }}</span></td>
            <td class="mono">{{ v.ip_address || '—' }}</td>
            <td class="mono">{{ v.vcpus ?? '—' }} / {{ v.mem_mib ?? '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No VMs placed on this node.</div>
    </div>

    <h2>Firewall <span class="muted" style="font-weight:400">(project rules for workloads placed here)</span></h2>
    <div class="card">
      <div v-if="fwRows.length">
        <table>
          <thead><tr><th>Project</th><th>Action</th><th>Proto</th><th>Port</th><th>Source</th></tr></thead>
          <tbody>
            <tr v-for="(r, i) in fwRows" :key="i">
              <td class="mono muted">{{ r.project }}</td>
              <td><span class="pill" :class="r.action === 'allow' ? 'ok' : 'bad'">{{ r.action }}</span></td>
              <td class="mono">{{ r.proto || 'any' }}</td>
              <td class="mono">{{ r.port || 'any' }}</td>
              <td class="mono">{{ r.source || 'any' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty">No firewall rules on the projects placed here.</div>
    </div>

    <h2>Daemon logs</h2>
    <div class="card mono" style="max-height:260px;overflow:auto;background:#1D1D1F;color:#E8E8ED;font-size:12px">
      <div v-for="l in logs" :key="l.id ?? l.ts" class="logline">
        <span class="muted" style="margin-right:8px">{{ fmtTs(l.ts) }}</span><span>{{ l.line }}</span>
      </div>
      <div v-if="!logs.length" class="muted">No daemon log lines matched this node.</div>
    </div>
  </div>
  <div v-else-if="err" class="empty">{{ err }} <button class="ghost" @click="load">Retry</button></div>
  <div v-else class="empty">Loading node…</div>
</template>
<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRoute } from 'vue-router'
import { get, ago, pillClass } from '../lib/api.js'
const route = useRoute()
const id = route.params.id
const d = ref(null), err = ref(''), logs = ref([]), fwRows = ref([])
const series = ref({})
const win = ref(1440)
const windows = [{ m: 60, label: '1h' }, { m: 360, label: '6h' }, { m: 1440, label: '24h' }]
let poll = null
onMounted(() => { load(); loadSeries(); poll = setInterval(loadSeries, 30000) })
onBeforeUnmount(() => clearInterval(poll))
async function load() {
  err.value = ''
  try {
    d.value = await get(`/servers/${id}/detail`)
    logs.value = (await get(`/servers/${id}/logs?limit=100`)).logs || []
    await loadFirewall()
  } catch (e) { err.value = e.message }
}
async function loadSeries() {
  try {
    const step = win.value <= 60 ? 60 : win.value <= 360 ? 300 : 900
    const r = await get(`/servers/${id}/analytics?minutes=${win.value}&step=${step}`)
    series.value = r.series || {}
  } catch { series.value = {} }
}
async function loadFirewall() {
  fwRows.value = []
  const projects = [...new Set((d.value.vms || []).map(v => v.project_id).filter(Boolean))]
  for (const pid of projects.slice(0, 5)) {
    try {
      const rules = await get(`/projects/${pid}/firewall/rules`)
      for (const r of Array.isArray(rules) ? rules : rules.rules || [])
        fwRows.value.push({ project: (d.value.vms.find(v => v.project_id === pid) || {}).project_name || pid.slice(0, 8), ...r })
    } catch { /* project may lack rules */ }
  }
}
function pts(code, max) {
  const arr = series.value[code] || []
  if (!arr.length) return ''
  const n = arr.length
  return arr.map((p, i) => `${(i / (n - 1)) * 400},${118 - (Math.min(Number(p.avg) || 0, max) / max) * 112}`).join(' ')
}
const line = (code, max) => pts(code, max || 1)
function yMax(code) {
  const arr = series.value[code] || []
  return Math.max(1, ...arr.map(p => Number(p.max) || 0))
}
function peak(code) {
  const arr = series.value[code] || []
  return arr.length ? Math.max(...arr.map(p => Number(p.max) || 0)).toFixed(1) : '—'
}
function seriesLabel(code) {
  const arr = series.value[code] || []
  return arr.length ? `${arr.length} samples · window ${win.value >= 1440 ? '24h' : win.value >= 360 ? '6h' : '1h'}` : 'No samples in this window yet'
}
function fmtMiB(v) { return v == null ? '—' : v >= 1024 ? (v / 1024).toFixed(1) + ' GiB' : v + ' MiB' }
function fmtTs(ts) {
  const t = Date.parse(ts)
  if (!t || Number.isNaN(t)) return ''
  return new Date(t).toLocaleTimeString()
}
function lastSeen(ts) {
  const t = Date.parse(ts)
  if (!t || Number.isNaN(t) || t < 946684800000) return 'never'
  return ago(ts)
}
</script>
<style scoped>
button.on{background:var(--blue);color:#fff}
</style>
