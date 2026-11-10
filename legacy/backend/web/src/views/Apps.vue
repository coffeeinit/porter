<template>
  <div>
    <div class="page-head">
      <div>
        <div class="row" style="gap:10px;flex-wrap:wrap">
          <h1>Services</h1>
          <span v-if="!loading && !error" class="pill info">{{ filtered.length }} total</span>
        </div>
        <p class="muted sub">Firecracker workloads grouped under Projects.</p>
      </div>
      <router-link to="/services/new" class="create">+ New service</router-link>
    </div>

    <div v-if="error" class="errbar" role="alert">{{ error }} <button class="ghost" @click="load">Retry</button></div>
    <div class="card toolbar">
      <input v-model="query" class="search" placeholder="Filter by service or project name…">
      <span class="muted count-note">{{ filtered.length }} shown</span>
    </div>

    <div v-if="loading" class="empty">Loading services…</div>
    <div v-else-if="!filtered.length && !error" class="empty">
      {{ query ? `No services match “${query}”.` : 'No services yet. Create a Project, then add a Service.' }}
    </div>
    <div v-else class="card table-card">
      <table>
        <thead><tr><th>Service</th><th>Project</th><th>State</th><th>Runtime</th><th class="num">Desired instances</th><th>Created</th></tr></thead>
        <tbody>
          <tr v-for="service in filtered" :key="service.id" class="service-row" @click="$router.push(`/services/${encodeURIComponent(service.id)}`)">
            <td><strong>{{ service.name }}</strong><div class="mono muted id">{{ service.id }}</div></td>
            <td>{{ service.projectName }}</td>
            <td><span class="pill" :class="pillClass(service.state)">{{ service.state }}</span></td>
            <td>Firecracker / KVM</td>
            <td class="num">{{ service.desired_instances }}</td>
            <td class="muted">{{ ago(service.created_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ago, listAll, pillClass } from '../lib/api.js'

const route = useRoute()
const services = ref([])
const query = ref('')
const loading = ref(true)
const error = ref('')

const filtered = computed(() => {
  const text = query.value.trim().toLowerCase()
  const projectID = String(route.query.project_id || '')
  return services.value.filter((service) => {
    if (projectID && service.project_id !== projectID) return false
    return !text || `${service.name} ${service.projectName}`.toLowerCase().includes(text)
  })
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const projects = await listAll('/projects')
    const groups = await Promise.all(projects.map(async (project) => {
      const items = await listAll(`/projects/${encodeURIComponent(project.id)}/services`)
      return items.map((service) => ({ ...service, projectName: project.name }))
    }))
    services.value = groups.flat().sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || a.id.localeCompare(b.id))
  } catch (cause) {
    error.value = cause.message || 'Could not load services.'
  } finally { loading.value = false }
}

onMounted(load)
</script>

<style scoped>
.page-head{display:flex;align-items:flex-end;justify-content:space-between;gap:16px;margin-bottom:16px;flex-wrap:wrap}.page-head h1{margin:0}.sub{font-size:12px;margin-top:4px}.create{display:inline-flex;align-items:center;background:var(--blue);color:#fff;border-radius:var(--r2);padding:8px 14px;font-weight:550;font-size:13px}.create:hover{background:var(--blue2)}.toolbar{display:flex;align-items:center;gap:14px;padding:10px 14px;margin-bottom:14px;flex-wrap:wrap}.toolbar input.search{max-width:380px}.count-note{font-size:11px;margin-left:auto}.table-card{padding:0;overflow:auto}.num{text-align:right}.id{font-size:10px;margin-top:3px}.service-row{cursor:pointer}.service-row:hover td{background:rgba(255,255,255,.03)}.errbar{display:flex;align-items:center;gap:10px;background:rgba(255,69,58,.08);border:1px solid rgba(255,69,58,.25);color:#c22a22;border-radius:var(--r2);padding:9px 12px;font-size:12px;margin-bottom:14px}.errbar button{padding:4px 10px;font-size:12px}
</style>
