<template>
  <div class="instances-page">
    <div class="page-head"><div><div class="eyebrow">OPERATE</div><h1>Instances</h1><p class="muted">Running copies of your services. Host and guest diagnostics are available in the operator view.</p></div><button class="ghost" @click="load">Refresh</button></div>
    <div v-if="error" class="notice">{{ error }} <button class="ghost" @click="load">Retry</button></div>
    <div class="instance-summary"><div class="card summary"><span>All instances</span><strong>{{ instances.length }}</strong></div><div class="card summary"><span>Running</span><strong class="good">{{ runningCount }}</strong></div><div class="card summary"><span>Attention</span><strong :class="attentionCount ? 'warn' : ''">{{ attentionCount }}</strong></div></div>
    <div class="card instance-table"><div class="table-head"><div><h2>Service instances</h2><p>Choose an instance to inspect operator details.</p></div><input v-model="query" placeholder="Filter instances" aria-label="Filter instances"></div><div class="table-scroll"><table><thead><tr><th>Instance</th><th>Service</th><th>Project</th><th>Status</th><th>Health</th><th>Size</th><th>Started</th><th></th></tr></thead><tbody><tr v-for="instance in filtered" :key="instance.id" class="instance-row" @click="$router.push(`/instances/${encodeURIComponent(instance.id)}`)"><td class="mono">{{ short(instance.id) }}</td><td>{{ instance.service_name || instance.name || 'Service' }}</td><td>{{ projectName(instance.project_id) }}</td><td><span class="pill" :class="pillClass(instance.state)">{{ instance.state || 'unknown' }}</span></td><td><span class="pill" :class="pillClass(instance.health_status)">{{ instance.health_status || '—' }}</span></td><td>{{ instance.vcpus ?? '—' }} vCPU · {{ instance.mem_mib ? `${instance.mem_mib} MiB` : '—' }}</td><td class="muted">{{ instance.started_at ? ago(instance.started_at) : '—' }}</td><td><span class="inspect">Inspect →</span></td></tr></tbody></table></div><div v-if="!filtered.length" class="empty">{{ query ? 'No instances match this filter.' : 'No instances are registered yet.' }}</div></div>
    <p class="scope-note">Production instances use Firecracker on KVM. Detailed host and agent diagnostics are in Fleet.</p>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { ago, get, pillClass } from '../lib/api.js'
const instances = ref([]), projects = ref([]), error = ref(''), query = ref('')
const runningCount = computed(() => instances.value.filter((item) => String(item.state).toLowerCase() === 'running').length)
const attentionCount = computed(() => instances.value.filter((item) => ['failed', 'crashed', 'unhealthy'].includes(String(item.state || item.health_status).toLowerCase())).length)
const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return instances.value.filter((instance) => !q || [instance.id, instance.name, instance.service_name, projectName(instance.project_id), instance.state].some((value) => String(value || '').toLowerCase().includes(q)))
})
const short = (id) => String(id || '').slice(0, 12) || '—'
function projectName(id) { return projects.value.find((project) => project.id === id)?.name || id || '—' }
async function load() {
  error.value = ''
  try {
    const [vmResult, projectResult] = await Promise.all([get('/vms?limit=200'), get('/projects?limit=200')])
    instances.value = Array.isArray(vmResult) ? vmResult : (vmResult?.vms || [])
    projects.value = Array.isArray(projectResult) ? projectResult : (projectResult?.projects || [])
  } catch (e) { error.value = e.message || 'Could not load instances.' }
}
onMounted(load)
</script>

<style scoped>
.eyebrow{font-size:9px;letter-spacing:.13em;color:#8192a7;font-weight:730}.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:12px}.page-head h1{margin:4px 0}.page-head p{font-size:10.5px}.page-head button{font-size:9px}.instance-summary{display:grid;grid-template-columns:repeat(3,1fr);gap:10px;margin-top:15px}.summary{padding:13px 15px}.summary span{display:block;font-size:9px;color:var(--ink3)}.summary strong{font-size:21px;display:block;margin-top:3px}.good{color:#83e2bb}.warn{color:#f3c27b}.instance-table{margin-top:12px;padding:0;overflow:hidden}.table-head{padding:14px 15px;display:flex;align-items:center;justify-content:space-between;gap:12px;border-bottom:1px solid var(--hair)}.table-head h2{margin:0 0 3px;font-size:11px}.table-head p{font-size:9px;color:var(--ink3)}.table-head input{max-width:200px;font-size:9px}.table-scroll{overflow:auto}.table-scroll table{min-width:780px}.table-scroll th{font-size:8px}.table-scroll td{font-size:9px}.instance-row{cursor:pointer}.instance-row:hover td{background:rgba(255,255,255,.025)}.instance-row .mono{color:#bac8d9}.instance-row .pill{font-size:7px;padding:2px 6px}.inspect{font-size:8px;color:#a8c5f4;white-space:nowrap}.instance-table>.empty{margin:12px}.scope-note{font-size:8.5px;color:var(--ink3);margin:10px 3px}.notice{margin:12px 0;color:#ffa2aa;font-size:10px}.notice button{margin-left:8px;font-size:9px}
@media(max-width:650px){.page-head{align-items:flex-start}.instance-summary{gap:6px}.summary{padding:11px}.summary strong{font-size:18px}.table-head{align-items:flex-start;flex-direction:column}.table-head input{max-width:none}.page-head p{max-width:310px}}
</style>
