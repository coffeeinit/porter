<template>
  <div>
    <div class="row" style="align-items:flex-start;gap:12px">
      <div>
        <h1>Images</h1>
        <p class="muted">Bootable Firecracker images from <span class="mono">data/images</span> and user-uploaded custom images.</p>
      </div>
      <div class="row" style="margin-left:auto">
        <button class="ghost" @click="load">Refresh</button>
        <button @click="openUpload">+ Add image</button>
      </div>
    </div>

    <div class="statrow" style="margin-top:14px">
      <div class="card stat"><div class="slabel">Total images</div><div class="snum">{{ images.length }}</div><div class="muted small">Catalog + persisted uploads</div></div>
      <div class="card stat"><div class="slabel">Ready</div><div class="snum">{{ readyCount }}</div><div class="muted small">Validated rootfs and kernel</div></div>
      <div class="card stat"><div class="slabel">Base images</div><div class="snum">{{ baseCount }}</div><div class="muted small">Managed or base manifests</div></div>
      <div class="card stat"><div class="slabel">Custom images</div><div class="snum">{{ customCount }}</div><div class="muted small">OCI, Docker, ZIP, or builder images</div></div>
    </div>

    <div class="card toolbar" style="margin-top:14px">
      <input v-model="query" class="search" placeholder="Search image name, reference, or description…" />
      <span class="muted count-note">Showing <strong>{{ filtered.length }}</strong> of {{ images.length }}</span>
    </div>

    <div v-if="error" class="errbar">{{ error }} <button class="ghost" @click="load">Retry</button></div>
    <div v-else-if="loading" class="empty">Loading image catalog…</div>
    <div v-else-if="!filtered.length" class="empty">No images found. Add a manifest to <span class="mono">data/images</span> or upload a custom microVM image.</div>

    <div v-else class="card" style="padding:0;overflow:auto">
      <table>
        <thead><tr><th>Name</th><th>Reference</th><th>Type</th><th>Status</th><th>Architecture</th><th class="num">vCPU</th><th class="num">Memory</th><th>Rootfs</th></tr></thead>
        <tbody>
          <tr v-for="im in filtered" :key="im.id || im.image || im.name">
            <td><strong>{{ im.name || 'Unnamed image' }}</strong><div class="muted small">{{ im.description || '—' }}</div></td>
            <td class="mono">{{ im.image || '—' }}</td>
            <td><span class="pill info">{{ imageType(im) }}</span></td>
            <td><span class="pill" :class="statusClass(im.status)">{{ im.status || 'unknown' }}</span></td>
            <td class="mono">{{ im.architecture || '—' }}</td>
            <td class="num">{{ im.vcpus || '—' }}</td>
            <td class="num">{{ im.mem_mib ? `${im.mem_mib} MiB` : '—' }}</td>
            <td class="mono small">{{ im.rootfs ? shortPath(im.rootfs) : '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="showUpload" class="overlay" @click.self="showUpload = false">
      <form class="card dialog" @submit.prevent="submitUpload">
        <h2 style="margin:0 0 4px">Add custom image</h2>
        <p class="muted small">Upload a ZIP containing <span class="mono">rootfs.ext4 + vmlinux</span>, or an OCI/Docker-save image plus a kernel.</p>
        <label class="flabel">Name</label>
        <input v-model="form.name" placeholder="my-image" required>
        <label class="flabel">Format</label>
        <select v-model="form.kind"><option value="zip">MicroVM ZIP</option><option value="oci">OCI / Docker image</option></select>
        <label class="flabel">Image file</label>
        <input type="file" @change="form.file = $event.target.files[0]" required>
        <template v-if="form.kind === 'oci'">
          <label class="flabel">Kernel (vmlinux, optional when server has one configured)</label>
          <input type="file" @change="form.kernel = $event.target.files[0]">
        </template>
        <div class="grid" style="grid-template-columns:1fr 1fr;margin-top:8px">
          <div><label class="flabel">vCPUs</label><input v-model.number="form.vcpus" type="number" min="1"></div>
          <div><label class="flabel">Memory (MiB)</label><input v-model.number="form.mem_mib" type="number" min="128" step="128"></div>
        </div>
        <div v-if="uploadError" class="empty" style="margin-top:10px;color:var(--red)">{{ uploadError }}</div>
        <div class="row" style="justify-content:flex-end;margin-top:14px"><button type="button" class="ghost" @click="showUpload = false">Cancel</button><button type="submit" :disabled="uploading">{{ uploading ? 'Uploading…' : 'Upload image' }}</button></div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, upload } from '../lib/api.js'

const images = ref([]), loading = ref(true), error = ref(''), query = ref('')
const showUpload = ref(false), uploading = ref(false), uploadError = ref('')
const form = ref({ name: '', kind: 'zip', file: null, kernel: null, vcpus: 1, mem_mib: 256 })
const filtered = computed(() => images.value.filter(im => {
  const q = query.value.trim().toLowerCase()
  return !q || [im.name, im.image, im.description, im.type].some(v => String(v || '').toLowerCase().includes(q))
}))
const readyCount = computed(() => images.value.filter(im => String(im.status).toLowerCase() === 'ready').length)
const baseCount = computed(() => images.value.filter(im => String(im.image || '').startsWith('base://') || im.type === 'base').length)
const customCount = computed(() => images.value.filter(im => String(im.image || '').startsWith('custom://') || String(im.type || '').startsWith('custom')).length)
const imageType = im => im.type || (String(im.image || '').startsWith('custom://') ? 'custom' : 'base')
const statusClass = s => ['ready', 'healthy', 'active'].includes(String(s).toLowerCase()) ? 'ok' : (['invalid', 'failed'].includes(String(s).toLowerCase()) ? 'bad' : 'warn')
const shortPath = p => String(p).split('/').slice(-2).join('/')

async function load() {
  loading.value = true; error.value = ''
  try {
    const d = await get('/images')
    images.value = Array.isArray(d) ? d : (d?.images || d?.items || [])
  } catch (e) { error.value = e.message || 'failed to load images' }
  loading.value = false
}
function openUpload() { uploadError.value = ''; showUpload.value = true }
async function submitUpload() {
  if (!form.value.file) return
  uploading.value = true; uploadError.value = ''
  const body = new FormData()
  body.append('name', form.value.name.trim()); body.append('file', form.value.file)
  body.append('vcpus', String(form.value.vcpus || 1)); body.append('mem_mib', String(form.value.mem_mib || 256))
  if (form.value.kernel) body.append('kernel', form.value.kernel)
  try {
    await upload(form.value.kind === 'oci' ? '/images/custom/oci' : '/images/custom', body)
    showUpload.value = false
    form.value = { name: '', kind: 'zip', file: null, kernel: null, vcpus: 1, mem_mib: 256 }
    await load()
  } catch (e) { uploadError.value = e.message || 'upload failed' }
  uploading.value = false
}
onMounted(load)
</script>

<style scoped>
.statrow { display:grid; grid-template-columns:repeat(4,1fr); gap:14px; }
.stat .slabel { font-size:11px; text-transform:uppercase; letter-spacing:.05em; color:var(--ink3); font-weight:600; }
.stat .snum { font-size:26px; font-weight:700; margin:2px 0; }
.small { font-size:12px; }
.num { text-align:right; }
th.num { text-align:right; }
@media (max-width:900px) { .statrow { grid-template-columns:repeat(2,1fr); } }
</style>
