<template>
  <section>
    <div class="page-head"><div><div class="eyebrow">OBSERVE</div><h1>Observability</h1><p class="muted">Telemetry records reported by agents and runtimes.</p></div></div>
    <p class="notice">Current transport reports agent/runtime logs and host/Firecracker-process metrics. It does not provide application stdout/stderr or distributed traces.</p>
    <div class="filters card">
      <label>Time window <select v-model="window" @change="load"><option value="1">Last 1 hour</option><option value="6">Last 6 hours</option><option value="24">Last 24 hours</option><option value="168">Last 7 days</option></select></label>
      <label>Node ID <input v-model="nodeId" placeholder="Any node" @change="load"></label>
      <label>Instance ID <input v-model="instanceId" placeholder="Any instance" @change="load"></label>
      <button class="ghost" :disabled="loading" @click="load">{{ loading ? 'Loading…' : 'Refresh' }}</button>
    </div>
    <nav class="tabs" aria-label="Observability views"><button v-for="view in views" :key="view.id" :class="{ active: tab === view.id }" @click="setTab(view.id)">{{ view.label }}</button></nav>
    <div v-if="tab === 'traces'" class="card state"><h2>Traces unavailable</h2><p>There is no trace transport or trace data in the current API. No spans or trace IDs are available.</p></div>
    <div v-else-if="loading" class="card state" aria-live="polite">Loading telemetry…</div>
    <div v-else-if="error" class="card state error" role="alert">{{ error }} <button class="ghost" @click="load">Retry</button></div>
    <template v-else-if="tab === 'overview'">
      <div class="stats"><div class="card"><span>Log records</span><strong>{{ logs.length }}</strong></div><div class="card"><span>Metric records</span><strong>{{ metrics.length }}</strong></div><div class="card"><span>Latest report</span><strong>{{ formatTime(latest?.reported_at) }}</strong></div></div>
      <div class="grid"><section class="card"><h2>Latest logs</h2><p v-if="!logs.length" class="empty">No log records reported for this filter and time window.</p><article v-for="item in logs.slice(0,5)" :key="item.id"><small>{{ formatTime(item.reported_at) }} · {{ item.level }}</small><p>{{ item.message }}</p><code>{{ item.node_id }}<span v-if="item.instance_id"> · {{ item.instance_id }}</span></code></article></section><section class="card"><h2>Latest metrics</h2><p v-if="!metrics.length" class="empty">No metric records reported for this filter and time window.</p><article v-for="item in metrics.slice(0,5)" :key="item.id"><small>{{ formatTime(item.reported_at) }}</small><p>{{ item.name }}: <b>{{ item.value }} {{ item.unit }}</b></p><code>{{ item.node_id }}<span v-if="item.instance_id"> · {{ item.instance_id }}</span></code></article></section></div>
    </template>
    <template v-else>
      <div v-if="tab === 'logs'" class="card table-wrap"><h2>Agent/runtime logs</h2><p class="muted">These records are not application stdout/stderr.</p><p v-if="!logs.length" class="empty">No log records reported for this filter and time window.</p><table v-else><thead><tr><th>Reported</th><th>Level</th><th>Message</th><th>Node</th><th>Instance</th></tr></thead><tbody><tr v-for="item in logs" :key="item.id"><td>{{ formatTime(item.reported_at) }}</td><td>{{ item.level }}</td><td>{{ item.message }}</td><td>{{ item.node_id }}</td><td>{{ item.instance_id || '—' }}</td></tr></tbody></table></div>
      <div v-else class="card table-wrap"><h2>Host / Firecracker-process metrics</h2><p class="muted">Metrics are not application-level metrics.</p><p v-if="!metrics.length" class="empty">No metric records reported for this filter and time window.</p><table v-else><thead><tr><th>Reported</th><th>Name</th><th>Value</th><th>Node</th><th>Instance</th></tr></thead><tbody><tr v-for="item in metrics" :key="item.id"><td>{{ formatTime(item.reported_at) }}</td><td>{{ item.name }}</td><td>{{ item.value }} {{ item.unit }}</td><td>{{ item.node_id }}</td><td>{{ item.instance_id || '—' }}</td></tr></tbody></table></div>
    </template>
  </section>
</template>
<script setup>
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { get } from '../lib/api.js'
const route = useRoute(), router = useRouter()
const views = [{ id: 'overview', label: 'Overview' }, { id: 'metrics', label: 'Metrics' }, { id: 'logs', label: 'Logs' }, { id: 'traces', label: 'Traces' }]
const valid = views.map(v => v.id), tab = ref(valid.includes(String(route.query.tab)) ? String(route.query.tab) : 'overview')
const window = ref('1'), nodeId = ref(''), instanceId = ref(''), loading = ref(false), error = ref(''), logs = ref([]), metrics = ref([])
const latest = computed(() => [...logs.value, ...metrics.value].sort((a,b) => Date.parse(b.reported_at)-Date.parse(a.reported_at))[0])
function setTab(id) { tab.value=id; const query={...route.query}; if(id==='overview') delete query.tab; else query.tab=id; router.replace({query}) }
function formatTime(value) { const d=Date.parse(value); return Number.isNaN(d) ? '—' : new Date(d).toLocaleString() }
async function load() {
  loading.value=true; error.value=''
  const to=new Date(), from=new Date(to.getTime()-Number(window.value)*3600000), params=new URLSearchParams({from:from.toISOString(),to:to.toISOString(),page_size:'100'})
  if(nodeId.value.trim()) params.set('node_id',nodeId.value.trim()); if(instanceId.value.trim()) params.set('instance_id',instanceId.value.trim())
  try { const [l,m]=await Promise.all([get(`/logs?${params}`),get(`/metrics?${params}`)]); if(!Array.isArray(l?.items)||!Array.isArray(m?.items)) throw new Error('The API returned an invalid telemetry page.'); logs.value=l.items; metrics.value=m.items }
  catch(e) { error.value=`Could not load telemetry — ${e.message || 'Unknown API error'}`; logs.value=[]; metrics.value=[] }
  finally { loading.value=false }
}
watch(() => route.query.tab, value => { tab.value=valid.includes(String(value)) ? String(value) : 'overview' })
load()
</script>
<style scoped>
.eyebrow{font-size:10px;letter-spacing:.12em;color:var(--ink2);font-weight:700}.notice{font-size:11px;color:var(--ink2);margin:10px 0;padding:10px;border-left:2px solid var(--orange);background:var(--panel)}.filters{display:flex;align-items:end;gap:10px;padding:12px;flex-wrap:wrap}.filters label{display:grid;gap:4px;font-size:10px;color:var(--ink2)}input,select{padding:7px;color:inherit;background:var(--bg);border:1px solid var(--line);border-radius:5px}.tabs{display:flex;gap:6px;margin:16px 0}.tabs button{padding:8px 12px}.tabs button.active{color:#fff;background:var(--accent)}.state{padding:20px;margin:12px 0}.error{color:#ff9ba5}.stats,.grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px}.stats .card,.grid .card{padding:14px}.stats span{display:block;color:var(--ink2);font-size:10px}.stats strong{display:block;margin-top:7px}.grid{grid-template-columns:repeat(2,minmax(0,1fr));margin-top:10px}.grid h2,.table-wrap h2{font-size:13px;margin:0 0 8px}.grid article{border-top:1px solid var(--soft);padding:8px 0}.grid small,.grid code{font-size:9px;color:var(--ink3)}.grid article p{font-size:11px;margin:4px 0;overflow-wrap:anywhere}.muted{font-size:10px;color:var(--ink3)}.table-wrap{padding:14px;overflow:auto}table{width:100%;border-collapse:collapse;font-size:10px}th,td{text-align:left;padding:9px 7px;border-bottom:1px solid var(--soft);vertical-align:top}td{overflow-wrap:anywhere}.empty{padding:14px;color:var(--ink3);font-size:11px}@media(max-width:700px){.stats,.grid{grid-template-columns:1fr}}
</style>
