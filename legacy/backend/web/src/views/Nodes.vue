<template>
  <section>
    <div class="row" style="align-items:flex-start">
      <div><h1>Fleet</h1><p class="muted">Persisted Fleet nodes and opt-in agent enrollment.</p></div>
      <button class="ghost" style="margin-left:auto" :disabled="loading" @click="load">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
    </div>

    <div class="card enrollment" style="margin-top:14px">
      <div class="row title-row">
        <div><h2>Enroll an agent</h2><p class="muted small">One-time token · expires in 10 minutes · v2 transport is opt-in</p></div>
        <span class="pill info">Manual setup only</span>
      </div>
      <p class="muted">Choose where the agent will run. For a single-host setup, select <b>Control-plane host</b>. Select <b>Remote host</b> only for another machine. This page never installs or starts an agent and never runs a remote command.</p>
      <div class="fields">
        <label>Agent location
          <select v-model="target">
            <option value="same_host">Control-plane host (single host)</option>
            <option value="remote_host">Another machine (remote agent)</option>
          </select>
        </label>
        <label>HTTPS control-plane origin
          <input v-model.trim="controlPlaneURL" type="url" placeholder="https://porter.example.com" autocomplete="url">
        </label>
      </div>
      <p class="muted small">Use the origin the selected agent host can reach, with a valid TLS certificate. Keep each Firecracker API socket local to its agent host.</p>
      <div class="row actions">
        <button :disabled="issuing || !controlPlaneURL" @click="issueToken">{{ issuing ? 'Creating…' : 'Create one-time enrollment token' }}</button>
        <span v-if="issueError" class="error-text" role="alert">{{ issueError }}</span>
      </div>

      <div v-if="issue" class="token-panel" role="status">
        <h3>Copy this token now</h3>
        <p class="muted">The token is shown only in this page session and cannot be retrieved again. It is valid until {{ formatTime(issue.expires_at) }} and can enroll one node once. If this session closes before redemption, wait for expiry and create another.</p>
        <code class="secret">{{ issue.token }}</code>
        <h4>On the selected agent host</h4>
        <ol>
          <li>Create a local file containing the token and restrict it to owner-only access (mode <code>0600</code>).</li>
          <li>Run the v2 join command locally. No remote install or start is performed by this page.</li>
        </ol>
        <code class="command">sudo porter-agent join --protocol-version v2 --server {{ issue.control_plane_url }} --enrollment-token-file /etc/porter/enrollment-token --data-dir /var/lib/porter-agent</code>
        <p class="muted small">After join, the server assigns the node ID and the agent stores its node credential in a protected local file. Starting v2 still requires a deliberate local command with <code>--enable-v2-dispatch=true</code>, an accessible KVM device, and Firecracker. Remote runtime/KVM readiness has not been verified.</p>
      </div>
    </div>

    <div v-if="error" class="empty" style="margin-top:14px" role="alert">Failed to load fleet nodes — {{ error }}</div>
    <div v-else class="card" style="padding:0;margin-top:14px">
      <div v-if="loading" class="empty" aria-live="polite">Loading fleet nodes…</div>
      <div v-else-if="!nodes.length" class="empty">No Fleet nodes have completed enrollment.</div>
      <table v-else>
        <thead><tr><th>Node</th><th>Location</th><th>State</th><th>Last seen</th><th>Created</th><th>Updated</th></tr></thead>
        <tbody>
          <tr v-for="node in nodes" :key="node.id">
            <td><div class="pname">{{ node.name }}</div><div class="mono muted small">{{ node.id }}</div></td>
            <td><span class="pill info">{{ targetLabel(node.enrollment_target) }}</span></td>
            <td><span class="pill" :class="pillClass(node.state)">{{ node.state }}</span></td>
            <td class="muted">{{ node.last_seen_at ? ago(node.last_seen_at) : '—' }}</td>
            <td class="muted">{{ formatTime(node.created_at) }}</td>
            <td class="muted">{{ formatTime(node.updated_at) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="nodes.length" class="band">{{ nodes.length }} node{{ nodes.length === 1 ? '' : 's' }} returned across all pages. Offline means no v2 heartbeat has been recorded yet.</div>
    </div>
    <p class="muted small" style="margin-top:12px">Enrollment uses PostgreSQL-backed v2 credentials and requires the operator to enable the v2 HTTPS transport and apply the explicit migrations. Legacy SQLite `/v1` enrollment remains unchanged.</p>
  </section>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { listAll, internalPost, ago, pillClass } from '../lib/api.js'

const nodes = ref([])
const loading = ref(true)
const error = ref('')
const target = ref('remote_host')
const controlPlaneURL = ref('')
const issuing = ref(false)
const issueError = ref('')
const issue = ref(null)

function formatTime(value) {
  if (!value) return '—'
  const time = Date.parse(value)
  return Number.isNaN(time) ? '—' : new Date(time).toLocaleString()
}
function targetLabel(value) {
  if (value === 'same_host') return 'Control-plane host'
  if (value === 'remote_host') return 'Remote agent'
  return 'Unspecified / legacy'
}
async function load() {
  loading.value = true
  error.value = ''
  try { nodes.value = await listAll('/fleet/nodes') } catch (err) { error.value = err.message || 'Unknown API error' } finally { loading.value = false }
}
async function issueToken() {
  issuing.value = true
  issueError.value = ''
  try {
    const result = await internalPost('/admin/fleet/enrollment-tokens', {
      target: target.value,
      control_plane_url: controlPlaneURL.value,
    })
    issue.value = result
  } catch (err) {
    issueError.value = err.status === 503
      ? 'Enrollment is unavailable. The operator must enable the v2 HTTPS transport and apply the PostgreSQL migrations first.'
      : (err.message || 'Could not create the enrollment token.')
  } finally {
    issuing.value = false
  }
}
onMounted(load)
</script>

<style scoped>
.small{font-size:11px}.pname{font-weight:600}.band{background:rgba(0,113,227,.05);border-top:1px solid var(--soft);padding:8px 12px;font-size:12px;color:var(--ink2);border-radius:0 0 var(--r) var(--r)}
.enrollment{padding:16px}.title-row{align-items:flex-start}.title-row h2{margin:0 0 4px}.fields{display:grid;grid-template-columns:minmax(190px, .8fr) minmax(260px, 1.2fr);gap:12px;margin:14px 0}.fields label{display:grid;gap:6px;font-size:12px;color:var(--ink2)}.fields input,.fields select{font:inherit;color:var(--ink);background:var(--bg);border:1px solid var(--soft);border-radius:8px;padding:9px 10px}.actions{align-items:center;gap:12px}.error-text{color:var(--danger,#b42318);font-size:12px}.token-panel{margin-top:16px;padding:14px;border:1px solid var(--soft);border-radius:10px;background:var(--bg2,rgba(0,113,227,.04))}.token-panel h3{margin:0 0 6px}.token-panel h4{margin:14px 0 6px}.token-panel ol{padding-left:20px;color:var(--ink2);font-size:12px}.secret,.command{display:block;overflow-wrap:anywhere;padding:10px;border:1px solid var(--soft);border-radius:8px;background:var(--bg);font-family:ui-monospace,SFMono-Regular,monospace}.secret{font-size:14px;user-select:all}.command{font-size:11px}@media(max-width:700px){.fields{grid-template-columns:1fr}.title-row{display:flex;flex-wrap:wrap}.command{font-size:10px}}
</style>
