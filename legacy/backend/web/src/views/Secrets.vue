<template>
  <div>
    <div class="row" style="justify-content:space-between;margin-bottom:14px">
      <h1 style="margin:0">Secrets</h1>
      <button @click="open = !open">{{ open ? 'Cancel' : 'New secret' }}</button>
    </div>
    <div class="card" v-if="open" style="margin-bottom:14px">
      <div class="grid" style="grid-template-columns:1fr 1fr auto;align-items:end">
        <div><label class="muted">Key</label><input v-model="form.key" placeholder="DATABASE_PASSWORD"></div>
        <div><label class="muted">Value (encrypted at rest, never shown again)</label><input v-model="form.value" type="password"></div>
        <button :disabled="!form.key || !form.value" @click="create">Create</button>
      </div>
      <div class="muted" style="margin-top:8px">{{ err }}</div>
    </div>
    <div class="card" v-if="projectRows.length">
      <label class="muted">Project</label>
      <select v-model="projectId" style="max-width:340px;margin:6px 0 12px">
        <option v-for="p in projectRows" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
      <table v-if="secrets.length">
        <thead><tr><th>Key</th><th>Value</th><th>Created</th><th></th></tr></thead>
        <tbody>
          <tr v-for="s in secrets" :key="sid(s)">
            <td class="mono">{{ s.key || s.Key || s.name }}</td>
            <td class="mono muted">●●●●●●●●</td>
            <td class="muted">{{ ago(s.created_at || s.CreatedAt) }}</td>
            <td><button class="danger" @click="remove(s)">Delete</button></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No secrets in this project yet.</div>
    </div>
    <div v-else class="empty">Create a project first.</div>
  </div>
</template>
<script setup>
import { ref, onMounted, watch } from 'vue'
import { get, post, del, ago } from '../lib/api.js'
const projectRows = ref([]), projectId = ref(''), secrets = ref([]), open = ref(false), err = ref('')
const form = ref({ key: '', value: '' })
const sid = (s) => s.id || s.ID
onMounted(load)
async function load() {
  projectRows.value = await get('/projects')
  if (projectRows.value.length && !projectId.value) projectId.value = projectRows.value[0].id
}
watch(projectId, loadSecrets)
async function loadSecrets() {
  if (!projectId.value) { secrets.value = []; return }
  try { secrets.value = await get(`/projects/${projectId.value}/secrets`) } catch { secrets.value = [] }
}
async function create() {
  err.value = ''
  try {
    await post(`/projects/${projectId.value}/secrets`, form.value)
    form.value = { key: '', value: '' }
    open.value = false
    loadSecrets()
  } catch (e) { err.value = e.message }
}
async function remove(s) {
  if (!confirm('Delete this secret?')) return
  await del(`/projects/${projectId.value}/secrets/${sid(s)}`).catch(e => err.value = e.message)
  loadSecrets()
}
</script>
