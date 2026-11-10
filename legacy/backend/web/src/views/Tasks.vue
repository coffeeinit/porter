<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Operations &amp; Events</h1>
        <p class="muted">Persisted operation status and the durable event stream.</p>
      </div>
      <button class="ghost" :disabled="loading" @click="load">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
    </div>

    <div class="card notice">
      This is a read-only record view. Agent enrollment, authentication, command polling, retry, cancel, and dispatch are unavailable. Lease tokens and command payloads are never shown here.
    </div>
    <div v-if="error" class="error-bar" role="alert">{{ error }}</div>
    <template v-else>
      <div class="grid kpis">
        <div class="card kpi"><span class="kpi-label">Recent operations</span><div class="kpi-value">{{ operations.length }}</div><span class="muted">latest page · maximum 50</span></div>
        <div class="card kpi"><span class="kpi-label">Queued</span><div class="kpi-value">{{ count('queued') }}</div><span class="muted">persisted, not dispatched</span></div>
        <div class="card kpi"><span class="kpi-label">Running / leased</span><div class="kpi-value">{{ count('running') }}</div><span class="muted">stored operation state</span></div>
        <div class="card kpi"><span class="kpi-label">Failed</span><div class="kpi-value">{{ count('failed') }}</div><span class="muted">terminal operation state</span></div>
      </div>

      <section class="card section" aria-labelledby="operations-title">
        <div class="section-head"><div><h2 id="operations-title">Recent operations</h2><p class="muted">Newest first. Command detail is limited to safe status metadata.</p></div></div>
        <div v-if="operations.length" class="table-wrap">
          <table>
            <thead><tr><th>Operation</th><th>Kind</th><th>Instance</th><th>Attempt</th><th>Command</th><th>Status</th><th>Updated</th></tr></thead>
            <tbody>
              <template v-for="operation in operations" :key="operation.id">
                <tr>
                  <td class="mono">{{ short(operation.id) }}</td>
                  <td><span class="pill info">{{ operation.kind }}</span></td>
                  <td class="mono muted">{{ short(operation.instance_id) }}</td>
                  <td>{{ operation.attempt }}</td>
                  <td><span class="pill" :class="pillClass(operation.command_state)">{{ operation.command_state }}</span></td>
                  <td><span class="pill" :class="pillClass(operation.state)">{{ operation.state }}</span></td>
                  <td class="muted">{{ ago(operation.updated_at) }}</td>
                  <td class="action"><button class="ghost mini" @click="toggle(operation.id)">{{ open === operation.id ? 'Hide' : 'Inspect' }}</button></td>
                </tr>
                <tr v-if="open === operation.id" class="detail-row">
                  <td colspan="8">
                    <div class="detail">
                      <div><span class="k">Project / Service</span><span class="mono">{{ operation.project_id }} / {{ operation.service_id }}</span></div>
                      <div><span class="k">Instance / Command</span><span class="mono">{{ operation.instance_id }} / {{ operation.command_id }}</span></div>
                      <div><span class="k">Lease expiry</span><span>{{ formatTime(operation.lease_expires_at) }}</span></div>
                      <div><span class="k">Error</span><span :class="operation.error ? 'err-txt' : 'muted'">{{ operation.error || 'none recorded' }}</span></div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">No operations have been recorded in PostgreSQL yet.</div>
        <p class="muted source">Source: <code>GET /api/v1/operations?page_size=50</code> · refreshed every 5 seconds.</p>
      </section>

      <section class="card section" aria-labelledby="events-title">
        <div class="section-head"><div><h2 id="events-title">Recent events</h2><p class="muted">Operation and command events are linked to their persisted resource IDs.</p></div></div>
        <div v-if="events.length" class="event-list">
          <div v-for="event in events" :key="event.id" class="event-row">
            <span class="event-dot"></span>
            <div class="event-body"><strong>{{ event.type }}</strong><small>{{ eventSummary(event) }}</small></div>
            <time class="muted">{{ ago(event.occurred_at) }}</time>
          </div>
        </div>
        <div v-else class="empty">No events have been recorded yet.</div>
        <p class="muted source">Source: <code>GET /api/v1/events?page_size=50</code>.</p>
      </section>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { get, ago, pillClass } from '../lib/api.js'

const operations = ref([])
const events = ref([])
const error = ref('')
const loading = ref(false)
const open = ref('')
let timer = null

const short = (value) => {
  const text = String(value || '—')
  return text.length > 22 ? `${text.slice(0, 19)}…` : text
}
const count = (state) => operations.value.filter((operation) => operation.state === state).length
const formatTime = (value) => value ? new Date(value).toLocaleString() : '—'
function toggle(id) { open.value = open.value === id ? '' : id }
function eventSummary(event) {
  const refs = [
    event.operation_id && `operation ${short(event.operation_id)}`,
    event.command_id && `command ${short(event.command_id)}`,
    event.instance_id && `instance ${short(event.instance_id)}`,
  ].filter(Boolean)
  const detail = event.data?.attempt ? `attempt ${event.data.attempt}` : (event.data?.result_state || '')
  return [refs.join(' · ') || 'Resource event', detail].filter(Boolean).join(' · ')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [operationPage, eventPage] = await Promise.all([
      get('/operations?page_size=50'),
      get('/events?page_size=50'),
    ])
    operations.value = Array.isArray(operationPage?.items) ? operationPage.items : []
    events.value = Array.isArray(eventPage?.items) ? eventPage.items : []
  } catch (e) {
    error.value = e.message || 'Failed to load PostgreSQL operations and events.'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 5000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.notice{margin:14px 0;padding:12px 14px;font-size:12px;color:var(--ink2);border-color:rgba(230,159,0,.3);background:rgba(230,159,0,.06)}
.kpis{grid-template-columns:repeat(auto-fit,minmax(170px,1fr));margin-top:14px}.kpi{display:flex;flex-direction:column;gap:4px}.kpi-label{font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:var(--ink3)}.kpi-value{font-size:24px;font-weight:600;letter-spacing:-.02em}
.section{margin-top:16px;padding:14px 16px}.section-head{margin-bottom:10px}.section-head h2{font-size:14px;margin:0 0 3px}.section-head p{font-size:11px;margin:0}.table-wrap{overflow-x:auto}table{width:100%;border-collapse:collapse;min-width:760px}th,td{text-align:left;padding:10px 8px;border-bottom:1px solid var(--soft);font-size:12px}th{color:var(--ink3);font-size:10px;text-transform:uppercase;letter-spacing:.05em}td.action{text-align:right}button.ghost.mini{padding:4px 10px;font-size:12px}.detail-row td{background:var(--solid)}.detail{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:10px;padding:5px 2px;font-size:12px}.detail .k{display:block;font-size:10px;text-transform:uppercase;letter-spacing:.05em;color:var(--ink3);margin-bottom:2px}
.event-list{display:flex;flex-direction:column}.event-row{display:flex;align-items:center;gap:10px;padding:10px 2px;border-bottom:1px solid var(--soft)}.event-row:last-child{border:0}.event-dot{width:7px;height:7px;border-radius:50%;background:#77869a;flex:none}.event-body{min-width:0;flex:1}.event-body strong,.event-body small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.event-body strong{font-size:11px}.event-body small,.event-row time{font-size:10px;color:var(--ink3);margin-top:2px}.source{font-size:11px;margin:10px 2px 0}.empty{padding:20px;text-align:center;color:var(--ink3);font-size:12px}.mono{font-family:var(--mono,ui-monospace,monospace)}.err-txt{color:#C22A22}.error-bar{margin-top:12px;padding:10px 12px;border-radius:var(--r2);background:rgba(255,69,58,.09);color:#C22A22;font-size:13px}
</style>
