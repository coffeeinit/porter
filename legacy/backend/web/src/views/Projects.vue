<template>
  <div>
    <div class="row page-head">
      <div>
        <h1>Projects</h1>
        <p class="muted">Group services by product or environment.</p>
      </div>
      <button style="margin-left:auto" @click="openCreate">+ New Project</button>
    </div>

    <div class="card stat-card">
      <div class="slabel">Projects</div>
      <div class="snum">{{ projects.length }}</div>
      <div class="muted small">Persisted in the PostgreSQL control plane</div>
    </div>

    <div v-if="error" class="empty error" role="alert">Could not load projects — {{ error }} <button class="ghost" @click="load">Retry</button></div>
    <div v-else-if="loading" class="empty">Loading projects…</div>
    <div v-else-if="!projects.length" class="empty">No projects yet. Create one to add a service.</div>

    <div v-else class="card table-card">
      <table>
        <thead><tr><th>Project</th><th>State</th><th>Created</th><th></th></tr></thead>
        <tbody>
          <tr v-for="project in projects" :key="project.id">
            <td>
              <router-link class="project-link" :to="`/projects/${encodeURIComponent(project.id)}`">{{ project.name }}</router-link>
              <div class="mono muted id">{{ project.id }}</div>
            </td>
            <td><span class="pill" :class="pillClass(project.state)">{{ project.state }}</span></td>
            <td class="muted">{{ ago(project.created_at) }}</td>
            <td><router-link class="small-link" :to="`/projects/${encodeURIComponent(project.id)}`">Open →</router-link></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="showCreate" class="overlay" @click.self="showCreate = false">
      <form class="card dialog" @submit.prevent="create">
        <h2>New Project</h2>
        <p class="muted small">This creates a Project record. Services and Releases are separate resources.</p>
        <label class="flabel" for="project-name">Name</label>
        <input id="project-name" v-model.trim="form.name" maxlength="128" required autocomplete="off" placeholder="payments-api">
        <div v-if="createError" class="empty error" role="alert">{{ createError }}</div>
        <div class="row actions">
          <button type="button" class="ghost" @click="showCreate = false">Cancel</button>
          <button type="submit" :disabled="creating">{{ creating ? 'Creating…' : 'Create Project' }}</button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ago, listAll, pillClass, post } from '../lib/api.js'

const router = useRouter()
const projects = ref([])
const loading = ref(true)
const error = ref('')
const showCreate = ref(false)
const creating = ref(false)
const createError = ref('')
const form = ref({ name: '' })

async function load() {
  loading.value = true
  error.value = ''
  try { projects.value = await listAll('/projects') }
  catch (cause) { error.value = cause.message || 'Request failed.' }
  finally { loading.value = false }
}

function openCreate() {
  form.value = { name: '' }
  createError.value = ''
  showCreate.value = true
}

async function create() {
  creating.value = true
  createError.value = ''
  try {
    const project = await post('/projects', { name: form.value.name.trim() })
    showCreate.value = false
    await load()
    await router.push(`/projects/${encodeURIComponent(project.id)}`)
  } catch (cause) {
    createError.value = cause.message || 'Could not create project.'
  } finally { creating.value = false }
}

onMounted(load)
</script>

<style scoped>
.page-head{align-items:flex-start;margin-bottom:14px}.page-head h1{margin:0}.page-head p{font-size:12px;margin-top:4px}.stat-card{display:inline-block;min-width:190px;padding:14px 18px;margin-bottom:14px}.slabel{font-size:10px;text-transform:uppercase;letter-spacing:.06em;color:var(--ink3);font-weight:650}.snum{font-size:25px;font-weight:700;font-variant-numeric:tabular-nums;margin:2px 0}.small{font-size:12px}.table-card{padding:0;overflow:auto}.project-link{font-weight:650}.id{font-size:10px;margin-top:3px}.small-link{font-size:11px}.error{color:var(--red)}.overlay{position:fixed;inset:0;background:rgba(0,0,0,.38);display:grid;place-items:center;padding:20px;z-index:20}.dialog{width:420px;max-width:100%;padding:20px}.dialog h2{margin:0 0 5px}.flabel{display:block;font-size:11px;font-weight:650;text-transform:uppercase;letter-spacing:.04em;color:var(--ink2);margin:15px 0 5px}.actions{justify-content:flex-end;margin-top:15px}
</style>
