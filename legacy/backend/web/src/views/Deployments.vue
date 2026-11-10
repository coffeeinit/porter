<template>
  <div class="deps">
    <div class="page-head">
      <div>
        <h1>Releases</h1>
        <p class="muted sub">Git release intent, immutable source identity, and Firecracker readiness.</p>
      </div>
      <div class="head-actions">
        <label class="proj-pick">
          <span class="muted">Project</span>
          <select v-model="projectId" @change="loadReleases">
            <option value="__all__">All projects</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </label>
        <button class="ghost" :disabled="loading" @click="loadReleases">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
      </div>
    </div>

    <div class="notice">
      Source refs are recorded as intent only. The isolated Firecracker builder is unavailable, so no Git ref is resolved, no immutable commit or artifact is claimed, and no host-side build fallback is used.
    </div>
    <div v-if="listErr" class="errbar">{{ listErr }} <button class="ghost" @click="loadReleases">Retry</button></div>

    <section class="card intent-panel">
      <div class="intent-head">
        <div>
          <h2>Record a Git release intent</h2>
          <p class="muted">This persists the requested ref and preview/production intent. It does not start a build or deploy.</p>
        </div>
      </div>
      <form class="intent-form" @submit.prevent="createIntent">
        <label>Service
          <select v-model="form.serviceId" required :disabled="!services.length">
            <option value="" disabled>Select a service</option>
            <option v-for="service in services" :key="service.id" :value="service.id">{{ service.projectName }} · {{ service.name }}</option>
          </select>
        </label>
        <label>Git repository
          <input v-model.trim="form.repository" type="url" placeholder="https://github.com/org/repo.git" required>
        </label>
        <label>Requested ref
          <input v-model.trim="form.ref" placeholder="main, a tag, or a commit ref" maxlength="255" required>
        </label>
        <label>Intent
          <select v-model="form.intent">
            <option value="preview">Preview</option>
            <option value="production">Production</option>
          </select>
        </label>
        <label>Dockerfile path
          <input v-model.trim="form.dockerfilePath" placeholder="Dockerfile" required>
        </label>
        <div class="form-action">
          <button type="submit" :disabled="saving || !services.length">{{ saving ? 'Recording…' : 'Record blocked intent' }}</button>
        </div>
      </form>
      <p v-if="formError" class="errtext" role="alert">{{ formError }}</p>
      <p v-if="formMessage" class="success-text" role="status">{{ formMessage }}</p>
      <p v-if="!loading && !services.length" class="muted small-note">Create a service before recording a release intent.</p>
    </section>

    <div class="split">
      <div class="list-col">
        <div v-if="loading" class="empty">Loading release records…</div>
        <div v-else-if="!releases.length && !listErr" class="empty">No PostgreSQL releases are recorded for this project.</div>
        <button v-for="release in releases" :key="release.id" class="card release-item" :class="{ sel: release.id === selectedId }" @click="select(release.id)">
          <div class="row between">
            <span class="mono rev">#{{ release.sequence }}</span>
            <span class="pill" :class="pillClass(release.state)">{{ release.state }}</span>
          </div>
          <div class="release-title">{{ release.serviceName }} · {{ release.projectName }}</div>
          <div class="row between meta">
            <span class="mono">{{ release.resolved_commit ? release.resolved_commit.slice(0, 8) : 'commit not pinned' }}</span>
            <span>{{ ago(release.created_at) }}</span>
          </div>
        </button>
      </div>

      <div class="detail-col">
        <div v-if="!selected" class="empty">Select a release to inspect its source and readiness state.</div>
        <template v-else>
          <div class="card panel-head">
            <div class="row between wrap">
              <div>
                <h2>Release #{{ selected.sequence }}</h2>
                <p class="muted sub2">{{ selected.serviceName }} · {{ selected.projectName }} · {{ fmtDate(selected.created_at) }}</p>
              </div>
              <span class="pill" :class="pillClass(selected.state)">{{ selected.state }}</span>
            </div>
          </div>

          <div v-if="selected.failure_message" class="errbar">{{ selected.failure_message }}</div>

          <h2>Source and build</h2>
          <div class="card">
            <table class="kv"><tbody>
              <tr><th>Intent</th><td><span class="pill" :class="selected.intent === 'production' ? 'warn' : 'info'">{{ selected.intent || 'preview' }}</span></td></tr>
              <tr><th>Repository</th><td class="mono break">{{ selected.source?.repository || '—' }}</td></tr>
              <tr><th>Requested ref</th><td class="mono">{{ selected.source?.ref || '—' }}</td></tr>
              <tr><th>Resolved commit</th><td class="mono break">{{ selected.resolved_commit || 'Not pinned — builder unavailable' }}</td></tr>
              <tr><th>Build kind</th><td>{{ selected.build?.kind || '—' }}</td></tr>
              <tr v-if="selected.build?.kind === 'dockerfile'"><th>Dockerfile</th><td class="mono">{{ selected.build?.dockerfile_path || '—' }}</td></tr>
              <tr v-if="selected.build?.kind === 'compose'"><th>Compose subset</th><td class="mono">{{ selected.build?.compose_file || '—' }} · service {{ selected.build?.primary_service || '—' }}</td></tr>
              <tr><th>Build state</th><td>{{ selected.state }}</td></tr>
            </tbody></table>
          </div>

          <h2>Durable operations</h2>
          <div class="card operation-card">
            <div v-if="operationsLoading" class="muted">Loading operation history…</div>
            <div v-else-if="operationError" class="errtext">{{ operationError }}</div>
            <div v-else-if="!operations.length" class="muted">No operation record yet.</div>
            <div v-for="operation in operations" :key="operation.id" class="operation-row">
              <div class="row between wrap">
                <strong>{{ operation.kind === 'production_promotion' ? 'Production promotion checks' : 'Build request' }}</strong>
                <span class="pill neutral">{{ operation.state }}</span>
              </div>
              <div class="muted small-note">
                <span v-if="operation.blocker">Blocker: <span class="mono">{{ operation.blocker }}</span> · </span>
                requested ref <span class="mono">{{ operation.requested_ref || selected.source?.ref }}</span>
              </div>
              <div v-if="operation.checks?.length" class="checks">
                <div v-for="check in operation.checks" :key="check.name" class="check-row">
                  <span class="pill" :class="check.passed ? 'ok' : 'bad'">{{ check.passed ? 'passed' : 'blocked' }}</span>
                  <strong>{{ check.name }}</strong><span class="muted">{{ check.detail }}</span>
                </div>
              </div>
              <div v-if="operation.builder_result" class="builder-receipt">
                <strong>Builder receipt · OCI image only</strong>
                <div class="mono break">{{ operation.builder_result.oci_reference }}</div>
                <div class="muted small-note">Digest {{ operation.builder_result.digest }} · {{ operation.builder_result.architecture }} · {{ operation.builder_result.size_bytes }} bytes · attempt {{ operation.builder_attempt }}</div>
                <div class="muted small-note">This does not prove a Firecracker guest boot, production health, or activation.</div>
              </div>
              <div v-else-if="operation.builder_error" class="muted small-note">{{ operation.builder_error }} · attempt {{ operation.builder_attempt }}</div>
              <div class="muted small-note">Operation {{ operation.id }} · {{ fmtDate(operation.created_at) }}</div>
            </div>
            <div class="operation-actions">
              <button class="ghost" :disabled="promoting" @click="checkPromotion">{{ promoting ? 'Checking…' : 'Run production promotion checks' }}</button>
              <span v-if="promotionMessage" class="muted small-note">{{ promotionMessage }}</span>
            </div>
          </div>

          <h2>Artifact readiness</h2>
          <div class="card">
            <table class="kv"><tbody>
              <tr><th>Artifact digest</th><td class="mono break">{{ selected.artifact_digest || 'Not produced' }}</td></tr>
              <tr><th>Guest-ready</th><td><span class="pill" :class="selected.guest_ready ? 'ok' : 'neutral'">{{ selected.guest_ready ? 'verified' : 'not verified' }}</span><span v-if="selected.guest_ready_at" class="muted time">{{ fmtDate(selected.guest_ready_at) }}</span></td></tr>
              <tr><th>Health gate</th><td><span class="pill" :class="gateClass(selected.health_gate_status)">{{ selected.health_gate_status || 'not_run' }}</span><span v-if="selected.health_gate_error" class="muted time">{{ selected.health_gate_error }}</span></td></tr>
              <tr><th>Health check time</th><td>{{ fmtDate(selected.health_gate_checked_at) }}</td></tr>
            </tbody></table>
            <p class="muted note">These are release-specific gates. A promotion check compares the exact pinned commit and artifact, the Firecracker guest-ready receipt, and health-gate result; activation is not wired.</p>
          </div>

          <h2>Build output</h2>
          <div class="empty">Build logs are unavailable in this slice. Agent-local paths and raw command output are not exposed.</div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, reactive, onMounted } from 'vue'
import { get, listAll, ago, pillClass, post } from '../lib/api.js'

const loading = ref(true)
const listErr = ref('')
const projects = ref([])
const services = ref([])
const projectId = ref('__all__')
const releases = ref([])
const selectedId = ref('')
const selected = computed(() => releases.value.find((item) => item.id === selectedId.value) || null)
const operations = ref([])
const operationsLoading = ref(false)
const operationError = ref('')
const saving = ref(false)
const promoting = ref(false)
const formError = ref('')
const formMessage = ref('')
const promotionMessage = ref('')
let pendingIntentKey = ''
let pendingPromotionKey = ''
const form = reactive({ serviceId: '', repository: '', ref: 'main', intent: 'preview', dockerfilePath: 'Dockerfile' })

function newIdempotencyKey(prefix) {
  const nonce = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`
  return `${prefix}-${nonce}`
}

function fmtDate(value) {
  const timestamp = Date.parse(value || '')
  return timestamp ? new Date(timestamp).toLocaleString() : '—'
}

function gateClass(status) {
  if (status === 'passed') return 'ok'
  if (status === 'failed') return 'bad'
  if (status === 'pending') return 'info'
  return 'neutral'
}

async function select(id) {
  selectedId.value = id
  promotionMessage.value = ''
  await loadOperations()
}

async function loadOperations() {
  const releaseId = selectedId.value
  operations.value = []
  operationError.value = ''
  if (!releaseId) return
  operationsLoading.value = true
  try {
    const page = await get(`/releases/${encodeURIComponent(releaseId)}/operations`)
    if (selectedId.value === releaseId) operations.value = Array.isArray(page?.items) ? page.items : []
  } catch (error) {
    if (selectedId.value === releaseId) operationError.value = error?.message || 'Could not load operation history.'
  } finally {
    operationsLoading.value = false
  }
}

async function createIntent() {
  formError.value = ''
  formMessage.value = ''
  if (!form.serviceId) {
    formError.value = 'Choose a service first.'
    return
  }
  saving.value = true
  if (!pendingIntentKey) pendingIntentKey = newIdempotencyKey('release')
  try {
    const result = await post(`/services/${encodeURIComponent(form.serviceId)}/releases`, {
      source: { kind: 'git', repository: form.repository, ref: form.ref },
      build: { kind: 'dockerfile', dockerfile_path: form.dockerfilePath },
      intent: form.intent,
      idempotency_key: pendingIntentKey,
    })
    pendingIntentKey = ''
    formMessage.value = result?.message || 'Release intent recorded as blocked; no build was started.'
    await loadReleases()
    if (result?.release?.id) {
      selectedId.value = result.release.id
      await loadOperations()
    }
  } catch (error) {
    formError.value = error?.message || 'Could not record the release intent.'
  } finally {
    saving.value = false
  }
}

async function checkPromotion() {
  if (!selected.value) return
  promotionMessage.value = ''
  promoting.value = true
  if (!pendingPromotionKey) pendingPromotionKey = newIdempotencyKey('promote')
  try {
    const result = await post(`/releases/${encodeURIComponent(selected.value.id)}/promote`, { idempotency_key: pendingPromotionKey })
    pendingPromotionKey = ''
    promotionMessage.value = result?.message || 'Promotion remains blocked; no production activation was started.'
    await loadOperations()
  } catch (error) {
    promotionMessage.value = error?.message || 'Could not record production promotion checks.'
  } finally {
    promoting.value = false
  }
}

async function loadReleases() {
  loading.value = true
  listErr.value = ''
  try {
    if (!projects.value.length) projects.value = await listAll('/projects')
    const visibleProjects = projectId.value === '__all__'
      ? projects.value
      : projects.value.filter((project) => project.id === projectId.value)
    const scopedServices = (await Promise.all(visibleProjects.map(async (project) => {
      const items = await listAll(`/projects/${encodeURIComponent(project.id)}/services`)
      return items.map((service) => ({ ...service, projectName: project.name }))
    }))).flat()
    services.value = scopedServices
    if (!form.serviceId || !scopedServices.some((service) => service.id === form.serviceId)) form.serviceId = scopedServices[0]?.id || ''
    const grouped = await Promise.all(scopedServices.map(async (service) => {
      const items = await listAll(`/services/${encodeURIComponent(service.id)}/releases`)
      return items.map((release) => ({ ...release, serviceName: service.name, projectName: service.projectName }))
    }))
    releases.value = grouped.flat().sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
    if (!releases.value.some((release) => release.id === selectedId.value)) selectedId.value = releases.value[0]?.id || ''
  } catch (error) {
    listErr.value = error?.message || 'Failed to load PostgreSQL releases.'
  } finally {
    loading.value = false
  }
  await loadOperations()
}

onMounted(loadReleases)
</script>

<style scoped>
.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:16px;margin-bottom:14px;flex-wrap:wrap}
h1{margin:0}.sub{font-size:12.5px;margin-top:4px}.head-actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
.proj-pick{display:flex;align-items:center;gap:8px;font-size:12px}.proj-pick select{width:auto;min-width:180px}
.notice{background:rgba(255,159,10,.1);border:1px solid rgba(255,159,10,.3);color:var(--ink);border-radius:var(--r2);padding:10px 13px;font-size:12.5px;margin-bottom:14px}
.intent-panel{padding:16px;margin-bottom:16px}.intent-head h2{margin:0;font-size:14px}.intent-head p{margin:5px 0 12px;font-size:11.5px}.intent-form{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:11px;align-items:end}.intent-form label{display:grid;gap:5px;font-size:11px;color:var(--ink2)}.intent-form input,.intent-form select{width:100%;min-width:0}.form-action{display:flex;align-items:center}.small-note{font-size:11px}.errtext{color:var(--red);font-size:12px;margin:10px 0 0}.success-text{color:var(--green);font-size:12px;margin:10px 0 0}
.split{display:grid;grid-template-columns:300px 1fr;gap:16px;align-items:start}.list-col{display:flex;flex-direction:column;gap:10px}
.release-item{display:block;width:100%;padding:13px 15px;text-align:left;cursor:pointer}.release-item:hover{border-color:rgba(0,113,227,.28)}.release-item.sel{border-color:var(--blue);box-shadow:0 0 0 1px var(--blue)}
.row{display:flex;align-items:center;gap:10px}.between{justify-content:space-between}.wrap{flex-wrap:wrap}.rev{font-weight:600}.release-title{font-size:13px;font-weight:600;margin-top:7px}.meta{font-size:11px;color:var(--ink2);margin-top:6px;font-variant-numeric:tabular-nums}
.pill.neutral{background:var(--soft);border-color:var(--hair);color:var(--ink2)}
.detail-col h2{font-size:13.5px;font-weight:650;margin:18px 0 8px}.panel-head{padding:16px 18px}.panel-head h2{margin:0;font-size:15px}.sub2{font-size:11.5px;margin-top:4px}.kv th{width:160px;border-bottom:1px solid var(--soft)}.kv td{border-bottom:1px solid var(--soft)}.break{word-break:break-all}.time{margin-left:8px;font-size:11px}.note{font-size:11.5px;margin-top:10px}.empty{padding:14px;color:var(--ink2);font-size:12.5px}
.operation-card{padding:14px}.operation-row+.operation-row{border-top:1px solid var(--soft);margin-top:12px;padding-top:12px}.operation-row>div:first-child{margin-bottom:7px}.checks{display:grid;gap:7px;margin:10px 0}.check-row{display:grid;grid-template-columns:auto minmax(100px,1fr) minmax(100px,2fr);gap:8px;align-items:center;font-size:11px}.builder-receipt{display:grid;gap:5px;margin:10px 0;padding:10px;border:1px solid var(--hair);border-radius:var(--r2);background:var(--soft);font-size:11px}.operation-actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-top:12px}.operation-actions button{font-size:11px}.operation-actions .small-note{margin:0}
.errbar{display:flex;align-items:center;gap:10px;background:rgba(255,69,58,.08);border:1px solid rgba(255,69,58,.25);color:#C22A22;border-radius:var(--r2);padding:9px 12px;font-size:12.5px;margin-bottom:14px}.errbar button{padding:4px 10px;font-size:12px}
@media(max-width:900px){.split{grid-template-columns:1fr}.check-row{grid-template-columns:auto 1fr}.check-row .muted{grid-column:1/-1}}
</style>
