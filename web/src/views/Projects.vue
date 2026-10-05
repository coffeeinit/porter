<template>
  <div>
    <div class="row" style="align-items:flex-start">
      <div>
        <h1>Projects &amp; Environments</h1>
        <p class="muted">MicroVM workload boundaries, images, and per-project deploy environments.</p>
      </div>
      <button style="margin-left:auto" @click="openCreate">+ New Project</button>
    </div>

    <div class="statrow" v-if="!error">
      <div class="card stat">
        <div class="slabel">Total Projects</div>
        <div class="snum">{{ projects.length }}</div>
        <div class="muted small">Active workload boundaries</div>
      </div>
      <div class="card stat">
        <div class="slabel">Desired Replicas</div>
        <div class="snum">{{ totalReplicas }}</div>
        <div class="muted small">Sum of replica pools</div>
      </div>
      <div class="card stat">
        <div class="slabel">Registered VMs</div>
        <div class="snum">{{ totalVMs }}</div>
        <div class="muted small">Across all projects</div>
      </div>
      <div class="card stat">
        <div class="slabel">Bootable Images</div>
        <div class="snum">{{ images.length }}</div>
        <div class="muted small">base:// and custom:// manifests</div>
      </div>
    </div>

    <div v-if="error" class="empty" style="margin-top:14px">Failed to load projects — {{ error }}</div>
    <div v-else-if="!loading && !projects.length" class="empty" style="margin-top:14px">No projects yet. Create your first project to deploy a microVM image.</div>

    <div class="card" style="margin-top:14px;padding:0" v-if="projects.length">
      <table>
        <thead>
          <tr>
            <th style="width:26px"></th>
            <th>Project</th>
            <th>Image</th>
            <th class="num">Replicas</th>
            <th class="num">VMs</th>
            <th>Source</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="p in projects" :key="p.id">
            <tr class="click" @click="toggle(p.id)">
              <td class="muted">{{ expanded === p.id ? '▾' : '▸' }}</td>
              <td>
                <div class="pname">{{ p.name }}</div>
                <div class="mono muted pid">{{ p.id }}</div>
              </td>
              <td class="mono">{{ p.image || '—' }}</td>
              <td class="num">{{ p.replicas_desired || p.replicas || 1 }}</td>
              <td class="num">{{ (p.vm_ids || []).length }}</td>
              <td><span class="pill info">{{ p.source || 'image' }}</span></td>
              <td class="muted">{{ ago(p.created_at) }}</td>
            </tr>
            <tr v-if="expanded === p.id">
              <td></td>
              <td colspan="6" style="padding:12px 10px 16px">
                <div class="slabel" style="margin-bottom:8px">Environments</div>
                <div v-if="envErr[p.id]" class="empty">Failed to load environments — {{ envErr[p.id] }}</div>
                <div v-else-if="envLoading[p.id]" class="empty">Loading environments…</div>
                <div v-else-if="!envs[p.id] || !envs[p.id].length" class="empty">No environments configured for this project.</div>
                <table v-else>
                  <thead><tr><th>Name</th><th>Branch</th><th>URL</th><th>Domain</th><th>Created</th></tr></thead>
                  <tbody>
                    <tr v-for="e in envs[p.id]" :key="e.id">
                      <td><span class="pill ok">{{ e.name }}</span></td>
                      <td class="mono">{{ e.branch || '—' }}</td>
                      <td class="mono">{{ e.url || '—' }}</td>
                      <td class="mono">{{ e.env_domain || '—' }}</td>
                      <td class="muted">{{ ago(e.created_at) }}</td>
                    </tr>
                  </tbody>
                </table>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
      <div class="band">Showing {{ projects.length }} project{{ projects.length === 1 ? '' : 's' }} · {{ totalReplicas }} desired replicas · {{ totalVMs }} registered VMs</div>
    </div>

    <div v-if="showCreate" class="overlay" @click.self="showCreate = false">
      <form class="card dialog" @submit.prevent="create">
        <h2 style="margin:0 0 4px">New Project</h2>
        <p class="muted small" style="margin-bottom:12px">Creates a microVM replica pool. The image must be a registered base:// or custom:// Firecracker manifest.</p>

        <label class="flabel" for="p-name">Name</label>
        <input id="p-name" v-model="form.name" placeholder="my-service" required>

        <label class="flabel" for="p-image">Image</label>
        <select id="p-image" v-model="form.image" required>
          <option value="" disabled>Select a bootable image…</option>
          <option v-for="im in images" :key="im.id || im.image" :value="im.image || ('base://' + im.name)">
            {{ im.name }} — {{ im.image || im.type }}{{ im.status ? ' (' + im.status + ')' : '' }}
          </option>
        </select>
        <div v-if="!images.length && !imgLoading" class="empty" style="margin-top:8px">No registered images available — {{ imgErr || 'register a base image first' }}</div>

        <div class="grid" style="grid-template-columns:1fr 1fr 1fr;margin-top:10px">
          <div>
            <label class="flabel" for="p-vcpus">vCPUs</label>
            <input id="p-vcpus" v-model.number="form.vcpus" type="number" min="1">
          </div>
          <div>
            <label class="flabel" for="p-mem">Memory (MiB)</label>
            <input id="p-mem" v-model.number="form.mem_mib" type="number" min="128" step="128">
          </div>
          <div>
            <label class="flabel" for="p-replicas">Replicas</label>
            <input id="p-replicas" v-model.number="form.replicas" type="number" min="1">
          </div>
        </div>

        <div v-if="createErr" class="empty" style="margin-top:12px;color:var(--red)">{{ createErr }}</div>
        <div class="row" style="justify-content:flex-end;margin-top:14px">
          <button type="button" class="ghost" @click="showCreate = false">Cancel</button>
          <button type="submit" :disabled="creating">{{ creating ? 'Creating…' : 'Create Project' }}</button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, post, ago } from '../lib/api.js'

const projects = ref([])
const images = ref([])
const loading = ref(true)
const error = ref('')
const expanded = ref('')
const envs = ref({})
const envLoading = ref({})
const envErr = ref({})

const imgLoading = ref(false)
const imgErr = ref('')
const showCreate = ref(false)
const creating = ref(false)
const createErr = ref('')
const form = ref({ name: '', image: '', vcpus: 1, mem_mib: 512, replicas: 1 })

const totalReplicas = computed(() => projects.value.reduce((s, p) => s + (p.replicas_desired || p.replicas || 1), 0))
const totalVMs = computed(() => projects.value.reduce((s, p) => s + (p.vm_ids || []).length, 0))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const d = await get('/projects')
    projects.value = Array.isArray(d) ? d : (d.projects || [])
  } catch (e) { error.value = e.message }
  loading.value = false
}

async function loadImages() {
  imgLoading.value = true
  imgErr.value = ''
  try {
    const d = await get('/images')
    images.value = (Array.isArray(d) ? d : []).filter(im => (im.image || '').startsWith('base://') || im.type === 'base' || (im.image || '').startsWith('custom://'))
  } catch (e) { imgErr.value = e.message }
  imgLoading.value = false
}

async function toggle(id) {
  expanded.value = expanded.value === id ? '' : id
  if (expanded.value !== id || envs.value[id] || envLoading.value[id]) return
  envLoading.value = { ...envLoading.value, [id]: true }
  try {
    envs.value = { ...envs.value, [id]: await get(`/projects/${id}/environments`) }
  } catch (e) { envErr.value = { ...envErr.value, [id]: e.message } }
  envLoading.value = { ...envLoading.value, [id]: false }
}

function openCreate() {
  createErr.value = ''
  showCreate.value = true
  if (!images.value.length && !imgLoading.value) loadImages()
}

async function create() {
  creating.value = true
  createErr.value = ''
  try {
    await post('/projects', {
      name: form.value.name.trim(),
      image: form.value.image,
      vcpus: form.value.vcpus || 1,
      mem_mib: form.value.mem_mib || 512,
      replicas: form.value.replicas || 1,
    })
    showCreate.value = false
    form.value = { name: '', image: '', vcpus: 1, mem_mib: 512, replicas: 1 }
    await load()
  } catch (e) { createErr.value = e.message }
  creating.value = false
}

onMounted(load)
</script>

<style scoped>
.statrow { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-top: 14px; }
.stat .slabel { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--ink3); font-weight: 600; }
.stat .snum { font-size: 26px; font-weight: 700; letter-spacing: -.02em; font-variant-numeric: tabular-nums; margin: 2px 0; }
.small { font-size: 12px; }
.num { text-align: right; }
th.num { text-align: right; }
.pname { font-weight: 600; }
.pid { font-size: 11px; }
.click { cursor: pointer; }
.click:hover td { background: var(--soft); }
.band { background: rgba(0,113,227,.05); border-top: 1px solid var(--soft); padding: 8px 12px; font-size: 12px; color: var(--ink2); border-radius: 0 0 var(--r) var(--r); }
.overlay { position: fixed; inset: 0; background: rgba(0,0,0,.32); display: grid; place-items: center; z-index: 20; }
.dialog { width: 460px; max-width: calc(100vw - 40px); background: #fff; }
.flabel { display: block; font-size: 11.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--ink2); margin: 10px 0 4px; }
</style>
