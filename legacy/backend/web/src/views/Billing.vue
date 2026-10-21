<template>
  <div>
    <h1 style="margin-bottom:4px">Billing &amp; Usage</h1>
    <p class="muted" style="margin-bottom:14px">Plans, subscriptions and invoice previews. Money movement stays outside Porter until a payment adapter is configured (by design).</p>
    <h2>Plans</h2>
    <div class="card">
      <table v-if="plans.length">
        <thead><tr><th>Plan</th><th>Monthly</th><th>Entitlements</th></tr></thead>
        <tbody>
          <tr v-for="p in plans" :key="p.id || p.ID">
            <td class="mono">{{ p.name || p.Name }}</td>
            <td class="mono">{{ fmtCents(p.monthly_cents ?? p.MonthlyCents) }}</td>
            <td class="muted">{{ (p.limits && Object.keys(p.limits).length) ? Object.entries(p.limits).map(([k, v]) => `${k}=${v}`).join(' · ') : 'open (no limits set)' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">{{ plansErr || 'No plans configured.' }}</div>
    </div>
    <h2>Per-project subscription &amp; invoice preview</h2>
    <div class="card" v-if="projectRows.length">
      <label class="muted">Project</label>
      <select v-model="projectId" style="max-width:340px;margin:6px 0 12px">
        <option v-for="p in projectRows" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
      <div class="grid" style="grid-template-columns:1fr 1fr">
        <div>
          <div class="muted" style="margin-bottom:6px">Subscription</div>
          <table v-if="sub">
            <tbody>
              <tr><td class="muted">Plan</td><td class="mono">{{ sub.plan_id || sub.PlanID || sub.plan }}</td></tr>
              <tr><td class="muted">State</td><td><span class="pill" :class="pillClass(sub.state || sub.State)">{{ sub.state || sub.State || 'none' }}</span></td></tr>
              <tr v-if="sub.current_period_start"><td class="muted">Period start</td><td class="mono">{{ sub.current_period_start }}</td></tr>
            </tbody>
          </table>
          <div v-else class="empty">{{ subErr || 'No subscription.' }}</div>
        </div>
        <div>
          <div class="muted" style="margin-bottom:6px">Invoice preview (usage-rated)</div>
          <table v-if="invoice && invoiceLines.length">
            <thead><tr><th>Line</th><th>Amount</th></tr></thead>
            <tbody>
              <tr v-for="(l, i) in invoiceLines" :key="i"><td class="mono">{{ l.label }}</td><td class="mono">{{ fmtCents(l.cents) }}</td></tr>
              <tr><td><b>Total</b></td><td class="mono"><b>{{ fmtCents(totalCents) }}</b></td></tr>
            </tbody>
          </table>
          <div v-else class="empty">{{ invErr || 'No invoice data.' }}</div>
        </div>
      </div>
    </div>
    <div v-else class="empty">Create a project to see subscription and invoice data.</div>
  </div>
</template>
<script setup>
import { ref, onMounted, computed } from 'vue'
import { get, pillClass } from '../lib/api.js'
const plans = ref([]), plansErr = ref(''), projectRows = ref([]), projectId = ref(''), sub = ref(null), subErr = ref(''), invoice = ref(null), invErr = ref('')
onMounted(load)
async function load() {
  try { plans.value = await get('/billing/plans') } catch (e) { plansErr.value = e.message; plans.value = [] }
  projectRows.value = await get('/projects').catch(() => [])
  if (projectRows.value.length) projectId.value = projectRows.value[0].id
  await loadProject()
}
async function loadProject() {
  sub.value = null; invoice.value = null; subErr.value = ''; invErr.value = ''
  if (!projectId.value) return
  try { sub.value = await get(`/projects/${projectId.value}/billing/subscription`) } catch (e) { subErr.value = e.message }
  try { invoice.value = await get(`/projects/${projectId.value}/billing/invoice`) } catch (e) { invErr.value = e.message }
}
const invoiceLines = computed(() => {
  const inv = invoice.value
  if (!inv) return []
  const raw = inv.lines || inv.Lines
  if (Array.isArray(raw)) return raw.map(l => ({ label: l.description || l.metric || l.Description || 'line', cents: Number(l.amount_cents ?? l.AmountCents ?? 0) }))
  return []
})
const totalCents = computed(() => Number(invoice.value?.total_cents ?? invoice.value?.TotalCents ?? 0) || invoiceLines.value.reduce((a, l) => a + l.cents, 0))
function fmtCents(c) { return '$' + ((Number(c) || 0) / 100).toFixed(2) }
</script>
