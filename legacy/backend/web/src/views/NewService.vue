<template>
  <div class="new-service">
    <nav class="crumbs"><router-link to="/services">Services</router-link><span>/</span><strong>New service</strong></nav>
    <div class="page-title">
      <div><div class="eyebrow">CREATE A SERVICE</div><h1>Configure a Firecracker service</h1><p class="muted">This saves service configuration to PostgreSQL. It does not build or launch a guest.</p></div>
    </div>

    <div class="form-layout">
      <form class="card source-form" @submit.prevent="create">
        <section class="form-section">
          <h2>Service details</h2>
          <label class="field-label">Service name<input v-model.trim="form.name" required maxlength="128" placeholder="inventory-api"></label>
          <label class="field-label">Project
            <select v-model="form.projectId" required :disabled="loadingProjects || !projects.length">
              <option value="" disabled>Select a project</option>
              <option v-for="project in projects" :key="project.id" :value="project.id">{{ project.name }}</option>
            </select>
          </label>
          <p v-if="projectError" class="field-hint error-text" role="alert">{{ projectError }}</p>
          <router-link v-if="!loadingProjects && !projects.length && !projectError" to="/projects" class="field-hint">Create a project first →</router-link>
        </section>

        <section class="form-section">
          <h2>Guest configuration</h2>
          <p class="muted section-note">The production workload runtime is Firecracker on KVM only.</p>
          <div class="runtime-fields">
            <label class="field-label">Application port<input v-model.number="form.port" type="number" min="1" max="65535" required></label>
            <label class="field-label">vCPU<input v-model.number="form.vcpus" type="number" min="1" max="64" required></label>
            <label class="field-label">Memory (MiB)<input v-model.number="form.memoryMB" type="number" min="128" step="128" required></label>
            <label class="field-label">Desired instances<input v-model.number="form.desiredInstances" type="number" min="1" max="50" required></label>
          </div>
        </section>

        <div v-if="createError" class="form-error" role="alert">{{ createError }}</div>
        <div class="form-actions">
          <router-link to="/services" class="subtle-link">Cancel</router-link>
          <button type="submit" :disabled="creating || loadingProjects || !projects.length">{{ creating ? 'Creating…' : 'Create service' }}</button>
        </div>
      </form>

      <aside class="card notice-card">
        <div class="eyebrow">SOURCE BUILDS</div>
        <h2>Not enabled yet</h2>
        <p>Git, Dockerfile, and Compose inputs will be connected only after disposable Firecracker builder MicroVMs pass isolation and cleanup checks.</p>
        <div class="notice">Creating a Service does not create a Release, run a build, or start an Instance. Release requests fail closed until the builder is available.</div>
      </aside>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listAll, post } from '../lib/api.js'

const route = useRoute()
const router = useRouter()
const projects = ref([])
const loadingProjects = ref(true)
const projectError = ref('')
const creating = ref(false)
const createError = ref('')
const form = ref({ name: '', projectId: '', port: 8080, vcpus: 1, memoryMB: 512, desiredInstances: 1 })

async function loadProjects() {
  loadingProjects.value = true
  projectError.value = ''
  try {
    projects.value = await listAll('/projects')
    const requestedProject = String(route.query.project_id || '')
    if (requestedProject && projects.value.some((project) => project.id === requestedProject)) form.value.projectId = requestedProject
    else if (!form.value.projectId && projects.value.length) form.value.projectId = projects.value[0].id
  } catch (cause) {
    projectError.value = cause.message || 'Could not load projects.'
  } finally { loadingProjects.value = false }
}

async function create() {
  if (!form.value.projectId) return
  creating.value = true
  createError.value = ''
  try {
    const service = await post(`/projects/${encodeURIComponent(form.value.projectId)}/services`, {
      name: form.value.name.trim(),
      port: Number(form.value.port),
      vcpus: Number(form.value.vcpus),
      memory_mb: Number(form.value.memoryMB),
      desired_instances: Number(form.value.desiredInstances),
      runtime: 'firecracker',
    })
    await router.push(`/services/${encodeURIComponent(service.id)}`)
  } catch (cause) {
    createError.value = cause.message || 'Could not create service.'
  } finally { creating.value = false }
}

onMounted(loadProjects)
</script>

<style scoped>
.crumbs{display:flex;gap:9px;align-items:center;font-size:11px;color:var(--ink3);margin-bottom:14px}.crumbs strong{color:var(--ink2)}.page-title{margin-bottom:18px}.page-title h1{margin:5px 0}.page-title p{font-size:12px}.eyebrow{font-size:9px;letter-spacing:.13em;color:#8293a8;font-weight:730}.form-layout{display:grid;grid-template-columns:minmax(0,1.2fr) minmax(270px,.8fr);align-items:start;gap:14px}.source-form{padding:0;overflow:hidden}.form-section{padding:20px;border-bottom:1px solid var(--hair)}.form-section h2,.notice-card h2{margin:0 0 5px;font-size:13px}.section-note,.field-hint{font-size:10px;color:var(--ink3);line-height:1.5}.field-label{display:block;font-size:10px;color:var(--ink2);margin-top:13px}.field-label input,.field-label select{display:block;margin-top:5px;font-size:11px}.runtime-fields{display:grid;grid-template-columns:1fr 1fr;gap:0 12px}.field-hint{display:block;margin-top:8px}.error-text,.form-error{color:var(--red)}.form-error{padding:10px 20px;font-size:12px}.form-actions{display:flex;justify-content:flex-end;align-items:center;gap:16px;padding:14px 20px}.form-actions button{font-size:11px;padding:9px 12px}.subtle-link{font-size:11px;color:var(--ink2)}.notice-card{padding:19px}.notice-card h2{margin:5px 0 10px}.notice-card p{font-size:11px;line-height:1.55;color:var(--ink2)}.notice{margin-top:14px;padding:11px;border-left:2px solid rgba(243,184,95,.55);background:rgba(243,184,95,.05);font-size:10px;line-height:1.5;color:#e8c47f}@media(max-width:820px){.form-layout{grid-template-columns:1fr}}
</style>
