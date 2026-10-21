<template>
  <div>
    <div class="row" style="justify-content:space-between;flex-wrap:wrap;gap:10px">
      <div>
        <h1>Tasks &amp; Operations</h1>
        <p class="muted">Durable workflow tasks from the operations ledger — non-terminal work the reconciler is converging.</p>
      </div>
      <button class="ghost" @click="load">Refresh</button>
    </div>

    <div v-if="error" class="error-bar">{{ error }}</div>
    <template v-else>
      <div class="grid kpis">
        <div class="card kpi">
          <span class="kpi-label">Active Operations</span>
          <div class="kpi-value">{{ ops.length }}</div>
          <span class="muted">queued · running · waiting · retrying</span>
        </div>
        <div class="card kpi">
          <span class="kpi-label">Retrying</span>
          <div class="kpi-value">{{ count('RETRYING') }}</div>
          <span class="muted">backed off, will re-drive</span>
        </div>
        <div class="card kpi">
          <span class="kpi-label">Running</span>
          <div class="kpi-value">{{ count('RUNNING') }}</div>
          <span class="muted">locked by a worker now</span>
        </div>
        <div class="card kpi">
          <span class="kpi-label">Queued / Waiting</span>
          <div class="kpi-value">{{ count('QUEUED') + count('WAITING') }}</div>
          <span class="muted">pending scheduler pickup</span>
        </div>
      </div>

      <div class="card" style="padding:0;margin-top:14px;overflow-x:auto">
        <table v-if="ops.length">
          <thead>
            <tr>
              <th>Task ID</th>
              <th>Kind</th>
              <th>Resource</th>
              <th>Attempt</th>
              <th>Status</th>
              <th>Updated</th>
              <th style="text-align:right">Detail</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="o in ops" :key="o.ID || o.id">
              <tr>
                <td class="mono">{{ short(o.ID || o.id) }}</td>
                <td><span class="pill info">{{ o.Kind || o.kind }}</span></td>
                <td class="mono muted">
                  {{ field(o, 'ResourceKind') || '?' }}<template v-if="field(o, 'ResourceID')"> · {{ short(field(o, 'ResourceID')) }}</template>
                </td>
                <td>{{ field(o, 'Attempt') ?? 0 }}</td>
                <td>
                  <span class="pill" :class="pillClass(state(o))">{{ state(o) }}</span>
                </td>
                <td class="muted">{{ ago(field(o, 'UpdatedAt')) }}</td>
                <td style="text-align:right">
                  <button class="ghost mini" @click="toggle(o.ID || o.id)">{{ open === (o.ID || o.id) ? 'Hide' : 'Inspect' }}</button>
                </td>
              </tr>
              <tr v-if="open === (o.ID || o.id)" class="detail-row">
                <td colspan="7">
                  <div class="detail">
                    <div><span class="k">Full task ID</span><span class="mono">{{ field(o, 'ID') }}</span></div>
                    <div><span class="k">Idempotency key</span><span class="mono">{{ field(o, 'IdempotencyKey') || '—' }}</span></div>
                    <div><span class="k">Resource</span><span class="mono">{{ field(o, 'ResourceKind') || '—' }} / {{ field(o, 'ResourceID') || '—' }}</span></div>
                    <div><span class="k">Error</span><span :class="field(o, 'Error') ? 'err-txt' : 'muted'">{{ field(o, 'Error') || 'none recorded' }}</span></div>
                    <div><span class="k">Updated</span><span>{{ field(o, 'UpdatedAt') || '—' }}</span></div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <div v-else class="empty">
          No active operations — the ledger only lists non-terminal tasks. Succeeded and failed tasks are pruned to the terminal states.
        </div>
      </div>
      <p class="muted" style="font-size:12px;margin-top:8px">
        Source: GET /operations · polled every 5s. Terminal (succeeded/failed) operations are not retained by this endpoint.
      </p>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { get, ago, pillClass } from '../lib/api.js'

const ops = ref([])
const error = ref('')
const open = ref('')

let timer = null

// The store's Operation struct has no json tags, so wire format uses Go
// field names (ID, Kind, State, …). Field helper keeps that in one place.
const field = (o, k) => o?.[k] ?? o?.[k.toLowerCase()] ?? ''
const state = (o) => String(field(o, 'State') || 'unknown').toLowerCase()
const short = (s) => (String(s).length > 18 ? `${String(s).slice(0, 15)}…` : s)
const count = (st) => ops.value.filter((o) => String(field(o, 'State')).toUpperCase() === st).length

function toggle(id) { open.value = open.value === id ? '' : id }

async function load() {
  error.value = ''
  try {
    const d = await get('/operations')
    ops.value = d?.operations || []
  } catch (e) {
    error.value = e.message || 'failed to load operations'
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 5000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.kpis { grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); margin-top: 16px }
.kpi { display: flex; flex-direction: column; gap: 4px }
.kpi-label { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3) }
.kpi-value { font-size: 24px; font-weight: 600; letter-spacing: -.02em }
button.ghost.mini { padding: 4px 10px; font-size: 12px }
.detail-row td { background: var(--solid); border-bottom: 1px solid var(--soft) }
.detail { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 10px; padding: 6px 2px; font-size: 12.5px }
.detail .k { display: block; font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3); margin-bottom: 2px }
.err-txt { color: #C22A22 }
.error-bar { margin-top: 12px; padding: 10px 12px; border-radius: var(--r2); background: rgba(255, 69, 58, .09); color: #C22A22; font-size: 13px }
</style>
