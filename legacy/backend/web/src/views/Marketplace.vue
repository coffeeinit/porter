<template>
  <div>
    <h1 style="margin-bottom:4px">Marketplace</h1>
    <p class="muted" style="margin-bottom:14px">One-click engine templates (databases, caches, object storage). Deploys as a MicroVM service — never Docker.</p>
    <div class="card" v-if="projectRows.length">
      <label class="muted">Deploy into project</label>
      <select v-model="projectId" style="max-width:340px;margin:6px 0 12px">
        <option v-for="p in projectRows" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
    </div>
    <div v-else class="empty" style="margin-bottom:14px">Create a project first to deploy templates.</div>
    <div class="grid" style="grid-template-columns:repeat(auto-fill,minmax(260px,1fr))">
      <div class="card" v-for="t in templates" :key="tname(t)">
        <div class="row" style="justify-content:space-between">
          <b>{{ tname(t) }}</b>
          <span class="pill info">{{ t.Version || t.version || '' }}</span>
        </div>
        <p class="muted" style="margin:8px 0 10px;min-height:34px">{{ t.Description || t.description }}</p>
        <div class="muted mono" style="font-size:12px;margin-bottom:10px">
          ports {{ (t.Ports || t.ports || []).join(', ') || '—' }} · volume {{ t.VolumeMiB ?? t.volume_mib ?? 0 }} MiB
        </div>
        <button :disabled="!projectId" @click="deploy(t)">Deploy</button>
      </div>
    </div>
    <div v-if="!templates.length" class="empty">{{ err || 'No templates registered.' }}</div>
    <div class="muted" style="margin-top:12px">{{ msg }}</div>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { get, post } from '../lib/api.js'
const templates = ref([]), projectRows = ref([]), projectId = ref(''), err = ref(''), msg = ref('')
const tname = (t) => t.Name || t.name
onMounted(load)
async function load() {
  try { templates.value = await get('/templates/services') } catch (e) { err.value = e.message; templates.value = [] }
  projectRows.value = await get('/projects').catch(() => [])
  if (projectRows.value.length) projectId.value = projectRows.value[0].id
}
async function deploy(t) {
  msg.value = ''
  try {
    await post(`/projects/${projectId.value}/services`, { template: tname(t) })
    msg.value = `${tname(t)} deployment queued.`
  } catch (e) { msg.value = e.message }
}
</script>
