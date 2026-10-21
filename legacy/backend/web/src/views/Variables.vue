<template>
  <div>
    <h1 style="margin-bottom:4px">Environment Variables</h1>
    <p class="muted" style="margin-bottom:14px">Plain config values. Secrets belong in the Secrets page and are injected encrypted.</p>
    <div class="card" v-if="projectRows.length">
      <label class="muted">Project</label>
      <select v-model="projectId" style="max-width:340px;margin:6px 0 12px">
        <option v-for="p in projectRows" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
      <table v-if="rows.length">
        <thead><tr><th>Key</th><th>Value</th><th></th></tr></thead>
        <tbody>
          <tr v-for="(r, i) in rows" :key="i">
            <td><input v-model="r.k" class="mono"></td>
            <td><input v-model="r.v"></td>
            <td><button class="ghost" @click="rows.splice(i, 1)">Remove</button></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No variables set.</div>
      <div class="row" style="margin-top:12px">
        <button class="ghost" @click="rows.push({ k: '', v: '' })">Add row</button>
        <button :disabled="saving" @click="save">{{ saving ? 'Saving…' : 'Save changes' }}</button>
        <span class="muted">{{ msg }}</span>
      </div>
    </div>
    <div v-else class="empty">Create a project first.</div>
  </div>
</template>
<script setup>
import { ref, onMounted, watch } from 'vue'
import { get, patch } from '../lib/api.js'
const projectRows = ref([]), projectId = ref(''), rows = ref([]), saving = ref(false), msg = ref('')
onMounted(load)
async function load() {
  projectRows.value = await get('/projects')
  if (projectRows.value.length && !projectId.value) projectId.value = projectRows.value[0].id
}
watch(projectId, loadEnv)
async function loadEnv() {
  if (!projectId.value) { rows.value = []; return }
  try {
    const d = await get(`/projects/${projectId.value}`)
    const env = (d.env || d.project?.env) || {}
    rows.value = Object.entries(env).map(([k, v]) => ({ k, v: String(v) }))
  } catch (e) { rows.value = []; msg.value = e.message }
}
async function save() {
  saving.value = true; msg.value = ''
  const env = {}
  for (const r of rows.value) if (r.k.trim()) env[r.k.trim()] = r.v
  try { await patch(`/projects/${projectId.value}`, { env }); msg.value = 'Saved.' }
  catch (e) { msg.value = e.message }
  saving.value = false
}
</script>
