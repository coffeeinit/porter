<template>
  <div>
    <nav class="crumbs"><router-link to="/services">Services</router-link><span>/</span><strong>{{ service?.name || 'Service' }}</strong></nav>
    <div v-if="loading" class="empty">Loading service…</div>
    <div v-else-if="error" class="empty error" role="alert">{{ error }} <button class="ghost" @click="load">Retry</button></div>
    <template v-else-if="service">
      <div class="page-head">
        <div><div class="eyebrow">SERVICE</div><h1>{{ service.name }}</h1><div class="mono muted id">{{ service.id }}</div></div>
        <span class="pill" :class="pillClass(service.state)">{{ service.state }}</span>
      </div>
      <div class="card detail-grid">
        <div><span class="slabel">Project</span><router-link v-if="project" :to="`/projects/${encodeURIComponent(project.id)}`">{{ project.name }}</router-link><span v-else>{{ service.project_id }}</span></div>
        <div><span class="slabel">Runtime</span><strong>Firecracker / KVM</strong></div>
        <div><span class="slabel">Application port</span><strong>{{ service.port }}</strong></div>
        <div><span class="slabel">vCPU</span><strong>{{ service.vcpus }}</strong></div>
        <div><span class="slabel">Memory</span><strong>{{ service.memory_mb }} MiB</strong></div>
        <div><span class="slabel">Desired instances</span><strong>{{ service.desired_instances }}</strong></div>
        <div><span class="slabel">Created</span><strong>{{ ago(service.created_at) }}</strong></div>
      </div>
      <div class="notice"><strong>Configuration only.</strong> This Service is persisted in PostgreSQL. No source build, Release, scheduling, or Firecracker guest has been started.</div>
      <div class="actions"><router-link class="small-link" :to="{ path: '/services/new', query: { project_id: service.project_id } }">Create another service →</router-link></div>
    </template>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ago, get, pillClass } from '../lib/api.js'

const route = useRoute()
const service = ref(null)
const project = ref(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  service.value = null
  project.value = null
  try {
    const id = encodeURIComponent(String(route.params.id || ''))
    const result = await get(`/services/${id}`)
    const parent = await get(`/projects/${encodeURIComponent(result.project_id)}`)
    service.value = result
    project.value = parent
  } catch (cause) {
    error.value = cause.message || 'Could not load service.'
  } finally { loading.value = false }
}

watch(() => route.params.id, load, { immediate: true })
</script>

<style scoped>
.crumbs{display:flex;gap:9px;align-items:center;font-size:11px;color:var(--ink3);margin-bottom:14px}.crumbs strong{color:var(--ink2)}.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:14px}.page-head h1{margin:4px 0}.eyebrow{font-size:9px;letter-spacing:.13em;color:#8293a8;font-weight:730}.id{font-size:10px}.detail-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:18px;margin-top:16px;padding:17px}.detail-grid>div{display:grid;gap:5px}.slabel{font-size:9px;text-transform:uppercase;letter-spacing:.06em;color:var(--ink3)}.detail-grid strong,.detail-grid a{font-size:12px}.notice{margin-top:14px;padding:12px 14px;border-left:2px solid rgba(243,184,95,.55);background:rgba(243,184,95,.05);font-size:11px;line-height:1.55;color:#e8c47f}.actions{margin-top:14px;text-align:right}.small-link{font-size:11px}.error{color:var(--red)}
</style>
