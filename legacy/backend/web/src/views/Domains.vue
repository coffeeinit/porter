<template>
  <div>
    <div class="row" style="align-items:flex-start">
      <div>
        <h1>Domains &amp; gateway routes</h1>
        <p class="muted">Manage domain ownership claims and saved route intent. No live traffic changes are applied.</p>
      </div>
      <button style="margin-left:auto" @click="openAdd">+ Claim domain</button>
    </div>

    <div class="notice" style="margin-top:14px">
      <b>Manual DNS required.</b> Add the TXT record shown for a pending claim at your DNS provider, then ask this app to check it. The app does not write provider DNS, automate ACME/TLS, or apply live gateway routing. Route controls below save desired intent only; gateway status remains unavailable.
    </div>

    <div v-if="error" class="empty" style="margin-top:14px">Could not load domains — {{ error }}</div>
    <div v-else-if="loading" class="empty" style="margin-top:14px">Loading projects and domain claims…</div>
    <div v-else-if="!claims.length" class="empty" style="margin-top:14px">No domain claims yet. Claim a hostname for a project service to begin.</div>

    <div v-if="claims.length" class="card" style="margin-top:14px;padding:0">
      <table>
        <thead><tr><th>Hostname</th><th>Project / service</th><th>Claim</th><th>Created</th><th></th></tr></thead>
        <tbody>
          <template v-for="c in claims" :key="c.id">
            <tr>
              <td class="mono dname">{{ c.hostname }}</td>
              <td>{{ projectName(c.project_id) }} / {{ serviceName(c.project_id, c.service_id) }}</td>
              <td><span class="pill" :class="c.state === 'verified' ? 'ok' : 'warn'">{{ c.state }}</span></td>
              <td class="muted">{{ ago(c.created_at) }}</td>
              <td style="text-align:right"><button v-if="c.state !== 'verified'" class="ghost" :disabled="verifying === c.id" @click="verify(c)">{{ verifying === c.id ? 'Checking DNS…' : 'Check TXT' }}</button><span v-else class="muted small">Proof saved</span></td>
            </tr>
            <tr v-if="c.state !== 'verified'">
              <td></td><td colspan="4" class="dns-instructions">
                <b>Publish this DNS TXT record manually:</b><br>
                Name: <code>{{ c.verification_txt_name }}</code><br>
                Value: <code>{{ c.ownership_proof_txt_value || c.verification_txt_value }}</code>
                <span v-if="verifyErrors[c.id]" class="err"> · {{ verifyErrors[c.id] }}</span>
              </td>
            </tr>
            <tr v-if="selectedClaim === c.id">
              <td></td><td colspan="4" class="route-area">
                <div class="row"><b>Saved route intents</b><button class="ghost" @click="openRoute(c)">+ Add path</button></div>
                <div v-if="routeError" class="empty err">Could not load routes — {{ routeError }}</div>
                <div v-else-if="routesLoading" class="muted small">Loading routes…</div>
                <div v-else-if="!routes.length" class="muted small">No route intents saved for this claim.</div>
                <table v-else class="nested"><thead><tr><th>Path prefix</th><th>Desired</th><th>Gateway status</th><th></th></tr></thead>
                  <tbody><tr v-for="r in routes" :key="r.id">
                    <td class="mono">{{ r.path_prefix }}</td>
                    <td><button class="ghost" :disabled="savingRoute === r.id || c.state !== 'verified'" @click="setDesired(r)">{{ savingRoute === r.id ? 'Saving…' : (r.desired_enabled ? 'Enabled' : 'Disabled') }}</button><div class="muted small">Desired intent only</div></td>
                    <td><span class="pill info">{{ r.gateway_status }}</span><div class="muted small">Not live</div></td><td><span v-if="routeErrors[r.id]" class="err">{{ routeErrors[r.id] }}</span></td>
                  </tr></tbody>
                </table>
                <p v-if="c.state !== 'verified'" class="muted small">Route intent cannot be enabled until this claim is verified.</p>
                <p class="muted small">Saved preview only — this does not configure an active gateway.</p>
              </td>
            </tr>
            <tr v-else><td></td><td colspan="4"><button class="ghost" @click="selectClaim(c)">View saved routes</button></td></tr>
          </template>
        </tbody>
      </table>
      <div class="band">{{ claims.length }} domain claim{{ claims.length === 1 ? '' : 's' }} · {{ verifiedCount }} verified</div>
    </div>

    <div v-if="showAdd" class="overlay" @click.self="showAdd = false">
      <form class="card dialog" @submit.prevent="add">
        <h2 style="margin:0 0 4px">Claim a hostname</h2>
        <p class="muted small">Claims require a manual DNS TXT ownership proof.</p>
        <label class="flabel" for="project">Project</label>
        <select id="project" v-model="form.project" required @change="loadServices"><option value="" disabled>Select a project…</option><option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name || p.id }}</option></select>
        <label class="flabel" for="service">Service</label>
        <select id="service" v-model="form.service" required :disabled="!form.project || servicesLoading"><option value="" disabled>{{ servicesLoading ? 'Loading services…' : 'Select a service…' }}</option><option v-for="s in services" :key="s.id" :value="s.id">{{ s.name || s.id }}</option></select>
        <label class="flabel" for="hostname">Hostname</label><input id="hostname" v-model="form.hostname" placeholder="api.example.com" required>
        <div v-if="addErr" class="empty err" style="margin-top:12px">{{ addErr }}</div>
        <div class="row" style="justify-content:flex-end;margin-top:14px"><button type="button" class="ghost" @click="showAdd = false">Cancel</button><button type="submit" :disabled="adding || !services.length">{{ adding ? 'Creating…' : 'Create claim' }}</button></div>
      </form>
    </div>

    <div v-if="showRoute" class="overlay" @click.self="showRoute = false"><form class="card dialog" @submit.prevent="addRoute">
      <h2 style="margin:0 0 4px">Add route intent</h2><p class="muted small">Creates a disabled saved preview; it does not apply live routing.</p>
      <label class="flabel" for="prefix">Path prefix</label><input id="prefix" v-model="pathPrefix" placeholder="/api" required>
      <div v-if="routeFormError" class="empty err">{{ routeFormError }}</div>
      <div class="row" style="justify-content:flex-end;margin-top:14px"><button type="button" class="ghost" @click="showRoute = false">Cancel</button><button type="submit" :disabled="addingRoute">{{ addingRoute ? 'Saving…' : 'Save disabled intent' }}</button></div>
    </form></div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { post, patch, listAll, ago } from '../lib/api.js'

const claims = ref([]), projects = ref([]), servicesByProject = ref({})
const loading = ref(true), error = ref(''), verifying = ref(''), verifyErrors = ref({})
const showAdd = ref(false), adding = ref(false), addErr = ref(''), services = ref([]), servicesLoading = ref(false)
const form = ref({ project: '', service: '', hostname: '' })
const selectedClaim = ref(''), routes = ref([]), routesLoading = ref(false), routeError = ref('')
const showRoute = ref(false), pathPrefix = ref(''), routeFormError = ref(''), addingRoute = ref(false)
const savingRoute = ref(''), routeErrors = ref({})
const verifiedCount = computed(() => claims.value.filter(c => c.state === 'verified').length)
const projectName = id => (projects.value.find(p => p.id === id) || {}).name || id
const serviceName = (pid, sid) => ((servicesByProject.value[pid] || []).find(s => s.id === sid) || {}).name || sid

async function load() {
  loading.value = true; error.value = ''
  try {
    const [ps, cs] = await Promise.all([listAll('/projects'), listAll('/domain-claims')])
    projects.value = ps; claims.value = cs
    const results = await Promise.all(ps.map(async p => [p.id, await listAll(`/projects/${encodeURIComponent(p.id)}/services`)]))
    servicesByProject.value = Object.fromEntries(results)
  } catch (e) { error.value = e.message }
  finally { loading.value = false }
}
async function loadServices() {
  form.value.service = ''; services.value = []; servicesLoading.value = true
  try { services.value = await listAll(`/projects/${encodeURIComponent(form.value.project)}/services`) }
  catch (e) { addErr.value = e.message }
  finally { servicesLoading.value = false }
}
function openAdd() { addErr.value = ''; form.value = { project: '', service: '', hostname: '' }; showAdd.value = true }
async function add() {
  adding.value = true; addErr.value = ''
  try {
    await post(`/projects/${encodeURIComponent(form.value.project)}/services/${encodeURIComponent(form.value.service)}/domain-claims`, { hostname: form.value.hostname.trim() })
    showAdd.value = false; await load()
  } catch (e) { addErr.value = e.message }
  finally { adding.value = false }
}
async function verify(c) {
  verifying.value = c.id; verifyErrors.value = { ...verifyErrors.value, [c.id]: '' }
  try {
    const updated = await post(`/domain-claims/${encodeURIComponent(c.id)}/verify-txt`, {})
    claims.value = claims.value.map(x => x.id === c.id ? updated : x)
  } catch (e) { verifyErrors.value = { ...verifyErrors.value, [c.id]: e.message } }
  finally { verifying.value = '' }
}
async function selectClaim(c) {
  selectedClaim.value = c.id; routes.value = []; routeError.value = ''; routesLoading.value = true
  try { routes.value = await listAll(`/domain-claims/${encodeURIComponent(c.id)}/routes`) }
  catch (e) { routeError.value = e.message }
  finally { routesLoading.value = false }
}
async function openRoute(c) { await selectClaim(c); pathPrefix.value = '/'; routeFormError.value = ''; showRoute.value = true }
async function addRoute() {
  addingRoute.value = true; routeFormError.value = ''
  try { await post(`/domain-claims/${encodeURIComponent(selectedClaim.value)}/routes`, { path_prefix: pathPrefix.value.trim() }); showRoute.value = false; await selectClaim(claims.value.find(c => c.id === selectedClaim.value)) }
  catch (e) { routeFormError.value = e.message }
  finally { addingRoute.value = false }
}
async function setDesired(r) {
  const c = claims.value.find(x => x.id === r.domain_claim_id)
  const desired = !r.desired_enabled
  if (desired && c?.state !== 'verified') return
  savingRoute.value = r.id; routeErrors.value = { ...routeErrors.value, [r.id]: '' }
  try {
    const updated = await patch(`/gateway-routes/${encodeURIComponent(r.id)}`, { desired_enabled: desired })
    routes.value = routes.value.map(x => x.id === r.id ? updated : x)
  } catch (e) { routeErrors.value = { ...routeErrors.value, [r.id]: e.status === 412 ? 'Claim must be verified before enabling this intent.' : e.message } }
  finally { savingRoute.value = '' }
}
onMounted(load)
</script>

<style scoped>
.notice { padding: 12px 14px; border: 1px solid var(--soft); border-radius: var(--r); background: rgba(0,113,227,.05); color: var(--ink2); font-size: 13px; line-height: 1.5; }
.dname { font-weight: 600; }
.dns-instructions { padding: 0 10px 12px; font-size: 12.5px; line-height: 1.7; }
.route-area { padding: 10px; }
.nested { margin-top: 8px; }
.err { color: var(--red); }
.small { font-size: 12px; }
.band { background: rgba(0,113,227,.05); border-top: 1px solid var(--soft); padding: 8px 12px; font-size: 12px; color: var(--ink2); border-radius: 0 0 var(--r) var(--r); }
.overlay { position: fixed; inset: 0; background: rgba(0,0,0,.32); display: grid; place-items: center; z-index: 20; }
.dialog { width: 440px; max-width: calc(100vw - 40px); background: var(--surface); }
.flabel { display: block; font-size: 11.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--ink2); margin: 10px 0 4px; }
</style>
