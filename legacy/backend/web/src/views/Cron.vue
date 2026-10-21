<template>
  <div>
    <div class="row" style="justify-content:space-between;margin-bottom:14px">
      <h1 style="margin:0">Cron &amp; Scheduled Jobs</h1>
      <button @click="open = !open">{{ open ? 'Cancel' : 'New cron' }}</button>
    </div>
    <div class="card" v-if="open" style="margin-bottom:14px">
      <div class="grid" style="grid-template-columns:1fr 1fr 1fr auto;align-items:end">
        <div><label class="muted">Name</label><input v-model="form.name" placeholder="nightly-backup"></div>
        <div><label class="muted">Schedule (cron)</label><input v-model="form.schedule" placeholder="0 3 * * *"></div>
        <div><label class="muted">Job image</label><input v-model="form.job_image" placeholder="base://alpine-3.21"></div>
        <button :disabled="!canSave" @click="create">Create</button>
      </div>
      <div class="muted" style="margin-top:8px">{{ err }}</div>
    </div>
    <div class="card" v-if="projectRows.length">
      <label class="muted">Project</label>
      <select v-model="projectId" style="max-width:340px;margin:6px 0 12px">
        <option v-for="p in projectRows" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
      <table v-if="crons.length">
        <thead><tr><th>Name</th><th>Schedule</th><th>Job image</th><th>State</th></tr></thead>
        <tbody>
          <tr v-for="c in crons" :key="cid(c)">
            <td class="mono">{{ c.Name || c.name }}</td>
            <td class="mono">{{ c.Schedule || c.schedule }}</td>
            <td class="mono muted">{{ c.JobImage || c.job_image }}</td>
            <td><span class="pill" :class="(c.Active ?? c.active) ? 'ok' : ''">{{ (c.Active ?? c.active) ? 'active' : 'paused' }}</span></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No crons in this project yet.</div>
    </div>
    <div v-else class="empty">Create a project first.</div>
  </div>
</template>
<script setup>
import { ref, onMounted, computed, watch } from 'vue'
import { get, post } from '../lib/api.js'
const projectRows = ref([]), projectId = ref(''), crons = ref([]), open = ref(false), err = ref('')
const form = ref({ name: '', schedule: '', job_image: 'base://alpine-3.21' })
const canSave = computed(() => form.value.name && form.value.schedule && form.value.job_image)
const cid = (c) => c.ID || c.id
onMounted(load)
async function load() {
  projectRows.value = await get('/projects')
  if (projectRows.value.length && !projectId.value) projectId.value = projectRows.value[0].id
}
watch(projectId, loadCrons)
async function loadCrons() {
  if (!projectId.value) { crons.value = []; return }
  try { crons.value = await get(`/projects/${projectId.value}/crons`) } catch { crons.value = [] }
}
async function create() {
  err.value = ''
  try {
    await post(`/projects/${projectId.value}/crons`, form.value)
    form.value = { name: '', schedule: '', job_image: 'base://alpine-3.21' }
    open.value = false
    loadCrons()
  } catch (e) { err.value = e.message }
}
</script>
