<template>
  <div>
    <div class="row" style="align-items:flex-start">
      <div>
        <h1>Domains &amp; DNS</h1>
        <p class="muted">Custom domains across every project in the org, with live DNS verification.</p>
      </div>
      <button style="margin-left:auto" @click="openAdd">+ Add Domain</button>
    </div>

    <div class="statrow" v-if="!error">
      <div class="card stat">
        <div class="slabel">Total Domains</div>
        <div class="snum">{{ domains.length }}</div>
        <div class="muted small">Configured across all projects</div>
      </div>
      <div class="card stat">
        <div class="slabel">Verified</div>
        <div class="snum ok">{{ verifiedCount }}</div>
        <div class="muted small">DNS resolves to the platform</div>
      </div>
      <div class="card stat">
        <div class="slabel">Pending / Unverified</div>
        <div class="snum">{{ domains.length - verifiedCount }}</div>
        <div class="muted small">Run verify to re-probe DNS</div>
      </div>
      <div class="card stat">
        <div class="slabel">DNS Records</div>
        <div class="snum">{{ records.length }}</div>
        <div class="muted small">{{ recProject ? 'Project: ' + recProjectName : 'Select a project to view records' }}</div>
      </div>
    </div>

    <div v-if="error" class="empty" style="margin-top:14px">Failed to load domains — {{ error }}</div>
    <div v-else-if="!loading && !domains.length" class="empty" style="margin-top:14px">No domains yet. Add a custom domain to a project to route traffic to it.</div>

    <div class="card" style="margin-top:14px;padding:0" v-if="domains.length">
      <table>
        <thead>
          <tr>
            <th>Domain</th>
            <th>Project</th>
            <th>Status</th>
            <th>Last probe</th>
            <th style="width:120px"></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(d, i) in domains" :key="d.project_id + d.domain">
            <tr>
              <td class="mono dname">{{ d.domain }}</td>
              <td>{{ d.project_name || d.project_id }}</td>
              <td><span class="pill" :class="d.verified ? 'ok' : 'warn'">{{ d.verified ? 'verified' : 'unverified' }}</span></td>
              <td class="muted">{{ ago(d.created_at) }}</td>
              <td style="text-align:right">
                <button class="ghost" style="padding:4px 10px;font-size:12.5px" :disabled="verifying === d.domain" @click="verify(d)">
                  {{ verifying === d.domain ? 'Probing…' : 'Verify' }}
                </button>
              </td>
            </tr>
            <tr v-if="probe && probe.domain === d.domain">
              <td></td>
              <td colspan="4" style="padding:0 10px 12px">
                <div class="probe" :class="probe.status === 'verified' ? 'pok' : 'pbad'">
                  <b>{{ probe.status }}</b> — {{ probe.detail }}
                  <span v-if="probe.records && probe.records.length" class="mono"> · A/AAAA: {{ probe.records.join(', ') }}</span>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
      <div class="band">Showing {{ domains.length }} domain{{ domains.length === 1 ? '' : 's' }} · {{ verifiedCount }} verified</div>
    </div>

    <h2>DNS Records</h2>
    <div class="card" style="padding:12px">
      <div class="row" style="margin-bottom:10px">
        <select v-model="recProject" style="max-width:280px" @change="loadRecords">
          <option value="" disabled>Select a project…</option>
          <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
        <span class="muted small">Records the platform expects you to publish for this project's domains.</span>
      </div>
      <div v-if="recErr" class="empty">Failed to load DNS records — {{ recErr }}</div>
      <div v-else-if="recLoading" class="empty">Loading records…</div>
      <div v-else-if="!recProject" class="empty">Select a project above to list its DNS records.</div>
      <div v-else-if="!records.length" class="empty">No DNS records registered for this project yet.</div>
      <table v-else>
        <thead><tr><th>Name</th><th>Type</th><th>Value</th><th class="num">TTL</th><th>Created</th></tr></thead>
        <tbody>
          <tr v-for="r in records" :key="r.id">
            <td class="mono">{{ r.name }}</td>
            <td><span class="pill info">{{ r.type }}</span></td>
            <td class="mono">{{ r.value }}</td>
            <td class="num">{{ r.ttl }}</td>
            <td class="muted">{{ ago(r.created_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="showAdd" class="overlay" @click.self="showAdd = false">
      <form class="card dialog" @submit.prevent="add">
        <h2 style="margin:0 0 4px">Add Custom Domain</h2>
        <p class="muted small" style="margin-bottom:12px">Attaches the domain to a project. Verification probes the domain's A/AAAA records.</p>

        <label class="flabel" for="d-project">Project</label>
        <select id="d-project" v-model="addForm.project" required>
          <option value="" disabled>Select a project…</option>
          <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>

        <label class="flabel" for="d-domain">Domain</label>
        <input id="d-domain" v-model="addForm.domain" placeholder="api.example.com" required>

        <label class="flabel" for="d-type">Type</label>
        <select id="d-type" v-model="addForm.type">
          <option value="apex">apex (root domain, A record)</option>
          <option value="subdomain">subdomain (CNAME)</option>
        </select>

        <div v-if="addErr" class="empty" style="margin-top:12px;color:var(--red)">{{ addErr }}</div>
        <div class="row" style="justify-content:flex-end;margin-top:14px">
          <button type="button" class="ghost" @click="showAdd = false">Cancel</button>
          <button type="submit" :disabled="adding">{{ adding ? 'Adding…' : 'Add Domain' }}</button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, post, ago } from '../lib/api.js'

const domains = ref([])
const projects = ref([])
const loading = ref(true)
const error = ref('')
const verifying = ref('')
const probe = ref(null)

const showAdd = ref(false)
const adding = ref(false)
const addErr = ref('')
const addForm = ref({ project: '', domain: '', type: 'subdomain' })

const recProject = ref('')
const recProjectName = computed(() => (projects.value.find(p => p.id === recProject.value) || {}).name || '')
const records = ref([])
const recLoading = ref(false)
const recErr = ref('')

const verifiedCount = computed(() => domains.value.filter(d => d.verified).length)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [d, p] = await Promise.all([get('/domains'), get('/projects')])
    domains.value = (d && d.domains) || []
    projects.value = Array.isArray(p) ? p : (p.projects || [])
    if (!recProject.value && projects.value.length) {
      recProject.value = projects.value[0].id
      loadRecords()
    }
  } catch (e) { error.value = e.message }
  loading.value = false
}

async function loadRecords() {
  if (!recProject.value) return
  recLoading.value = true
  recErr.value = ''
  try {
    const d = await get(`/projects/${recProject.value}/domains/records`)
    records.value = Array.isArray(d) ? d : (d.records || [])
  } catch (e) { recErr.value = e.message }
  recLoading.value = false
}

async function verify(d) {
  verifying.value = d.domain
  probe.value = null
  try {
    probe.value = await post('/domains/verify', { domain: d.domain })
    d.verified = probe.value.status === 'verified'
  } catch (e) {
    probe.value = { domain: d.domain, status: 'unverified', detail: e.message }
  }
  verifying.value = ''
}

function openAdd() {
  addErr.value = ''
  if (!projects.value.length && !error.value) load()
  showAdd.value = true
}

async function add() {
  adding.value = true
  addErr.value = ''
  try {
    await post(`/projects/${addForm.value.project}/domains`, {
      domain: addForm.value.domain.trim(),
      type: addForm.value.type,
    })
    showAdd.value = false
    addForm.value = { project: '', domain: '', type: 'subdomain' }
    await load()
  } catch (e) { addErr.value = e.message }
  adding.value = false
}

onMounted(load)
</script>

<style scoped>
.statrow { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-top: 14px; }
.stat .slabel { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3); font-weight: 600; }
.stat .snum { font-size: 26px; font-weight: 700; letter-spacing: -.02em; font-variant-numeric: tabular-nums; margin: 2px 0; }
.stat .snum.ok { color: #1B873B; }
.small { font-size: 12px; }
.num { text-align: right; }
th.num { text-align: right; }
.dname { font-weight: 600; }
.band { background: rgba(0,113,227,.05); border-top: 1px solid var(--soft); padding: 8px 12px; font-size: 12px; color: var(--ink2); border-radius: 0 0 var(--r) var(--r); }
.probe { font-size: 12.5px; padding: 8px 10px; border-radius: var(--r2); }
.probe.pok { background: rgba(48,209,88,.12); color: #1B873B; }
.probe.pbad { background: rgba(255,159,10,.12); color: #9A6200; }
.overlay { position: fixed; inset: 0; background: rgba(0,0,0,.32); display: grid; place-items: center; z-index: 20; }
.dialog { width: 440px; max-width: calc(100vw - 40px); background: #fff; }
.flabel { display: block; font-size: 11.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--ink2); margin: 10px 0 4px; }
</style>
