<template>
  <div>
    <nav class="crumbs"><router-link to="/projects">Projects</router-link><span>/</span><strong>{{ project?.name || 'Project' }}</strong></nav>
    <div v-if="loading" class="empty">Loading project…</div>
    <div v-else-if="error" class="empty error" role="alert">{{ error }} <button class="ghost" @click="load">Retry</button></div>
    <template v-else-if="project">
      <div class="page-head">
        <div><div class="eyebrow">PROJECT</div><h1>{{ project.name }}</h1><div class="mono muted id">{{ project.id }}</div></div>
        <router-link class="create" :to="{ path: '/services/new', query: { project_id: project.id } }">+ New service</router-link>
      </div>
      <div class="card project-meta">
        <div><span class="slabel">State</span><span class="pill" :class="pillClass(project.state)">{{ project.state }}</span></div>
        <div><span class="slabel">Created</span><span>{{ ago(project.created_at) }}</span></div>
        <div><span class="slabel">Services</span><strong>{{ services.length }}</strong></div>
      </div>
      <div class="row section-head"><h2>Services</h2><router-link :to="{ path: '/services', query: { project_id: project.id } }">View all →</router-link></div>
      <div v-if="!services.length" class="empty">No services in this Project yet.</div>
      <div v-else class="card table-card">
        <table><thead><tr><th>Service</th><th>State</th><th>Runtime</th><th class="num">Desired instances</th></tr></thead>
          <tbody><tr v-for="service in services" :key="service.id">
            <td><router-link :to="`/services/${encodeURIComponent(service.id)}`">{{ service.name }}</router-link><div class="mono muted id">{{ service.id }}</div></td>
            <td><span class="pill" :class="pillClass(service.state)">{{ service.state }}</span></td>
            <td>Firecracker / KVM</td><td class="num">{{ service.desired_instances }}</td>
          </tr></tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ago, get, listAll, pillClass } from '../lib/api.js'

const route = useRoute()
const project = ref(null)
const services = ref([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  project.value = null
  try {
    const id = encodeURIComponent(String(route.params.id || ''))
    const result = await get(`/projects/${id}`)
    const serviceItems = await listAll(`/projects/${id}/services`)
    project.value = result
    services.value = serviceItems
  } catch (cause) {
    error.value = cause.message || 'Could not load project.'
  } finally { loading.value = false }
}

watch(() => route.params.id, load, { immediate: true })
</script>

<style scoped>
.crumbs{display:flex;gap:9px;align-items:center;font-size:11px;color:var(--ink3);margin-bottom:14px}.crumbs strong{color:var(--ink2)}.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:14px;flex-wrap:wrap}.page-head h1{margin:4px 0}.eyebrow{font-size:9px;letter-spacing:.13em;color:#8293a8;font-weight:730}.id{font-size:10px}.create{display:inline-flex;align-items:center;background:var(--blue);color:#fff;border-radius:var(--r2);padding:8px 13px;font-size:12px}.project-meta{display:flex;gap:28px;flex-wrap:wrap;margin:16px 0;padding:14px}.project-meta>div{display:grid;gap:4px}.slabel{font-size:9px;text-transform:uppercase;letter-spacing:.06em;color:var(--ink3)}.section-head{justify-content:space-between;margin:20px 0 10px}.section-head h2{margin:0;font-size:14px}.section-head a{font-size:11px}.table-card{padding:0;overflow:auto}.num{text-align:right}.error{color:var(--red)}
</style>
