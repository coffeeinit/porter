<template>
  <div class="home">
    <!-- Intro banner -->
    <div class="card intro">
      <div>
        <h2>Workspace overview</h2>
        <p>Everything you need to build, deploy and operate your applications.</p>
      </div>
      <div class="context">
        <span>Workspace</span>
        <strong>{{ orgName || '—' }}</strong>
      </div>
    </div>

    <div v-if="err" class="errbar">{{ err }} <button class="ghost" @click="load">Retry</button></div>

    <div v-if="loading" class="empty">Loading workspace…</div>

    <template v-else>
      <!-- Live stat KPIs -->
      <div class="stats">
        <div class="card stat">
          <div class="label">Applications</div>
          <div class="value">{{ ov.projects ?? '—' }}</div>
          <div class="meta">{{ runningApps >= 0 ? `${runningApps} with running VMs` : '' }}</div>
        </div>
        <div class="card stat">
          <div class="label">MicroVMs</div>
          <div class="value">{{ ov.total_vms ?? '—' }}</div>
          <div class="meta" :class="runningCls">{{ ov.running ?? 0 }} running</div>
        </div>
        <div class="card stat">
          <div class="label">Deployments</div>
          <div class="value">{{ deployCount }}</div>
          <div class="meta">across {{ ov.projects ?? 0 }} applications</div>
        </div>
        <div class="card stat">
          <div class="label">Servers</div>
          <div class="value">{{ serverCount }}</div>
          <div class="meta">{{ onlineServers }} online</div>
        </div>
        <div class="card stat">
          <div class="label">Host</div>
          <div class="value small mono">{{ ov.hostname || ov.host || '—' }}</div>
          <div class="meta">v{{ ov.version || '—' }} · up {{ uptime }}</div>
        </div>
      </div>

      <!-- Quick actions -->
      <section class="section">
        <div class="section-title">
          <h3>Quick actions</h3>
          <span>{{ navCards.length }} areas</span>
        </div>
        <div class="card-grid">
          <router-link v-for="c in navCards" :key="c.to" class="card nav-card" :to="c.to">
            <span v-if="c.count !== ''" class="count">{{ c.count }}</span>
            <span class="nav-icon" v-html="c.icon"></span>
            <h4>{{ c.title }}</h4>
            <p>{{ c.desc }}</p>
          </router-link>
        </div>
      </section>

      <!-- Recent activity -->
      <section class="section">
        <div class="section-title">
          <h3>Recent activity</h3>
          <span>health events across the workspace</span>
        </div>
        <div class="card">
          <table v-if="events.length">
            <thead>
              <tr><th>Status</th><th>Detail</th><th>VM</th><th style="text-align:right">When</th></tr>
            </thead>
            <tbody>
              <tr v-for="e in events" :key="e.id">
                <td><span class="pill" :class="pillClass(e.status)">{{ e.status || 'info' }}</span></td>
                <td>{{ e.detail || '—' }}</td>
                <td class="mono">{{ e.vm_id || '—' }}</td>
                <td class="muted" style="text-align:right">{{ ago(e.ts) }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="empty">No recent events recorded yet.</div>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { get, ago, pillClass } from '../lib/api.js'

const loading = ref(true)
const err = ref('')
const ov = ref({})
const projects = ref([])
const servers = ref([])
const events = ref([])
const deployCount = ref('—')
const orgName = ref('')

const runningApps = computed(() => projects.value.filter((p) => (p.vm_ids || []).length > 0).length)
const serverCount = computed(() => servers.value.length)
const onlineServers = computed(() => servers.value.filter((s) => s.status === 'online' || s.status === 'registered').length)
const runningCls = computed(() => {
  const total = Number(ov.value.total_vms || 0)
  if (!total) return ''
  const r = Number(ov.value.running || 0)
  if (r === total) return 'good'
  if (r === 0) return 'bad'
  return 'warn'
})
const uptime = computed(() => {
  const s = Number(ov.value.uptime || 0)
  if (!s) return '—'
  if (s < 3600) return `${s / 60 | 0}m`
  if (s < 86400) return `${s / 3600 | 0}h`
  return `${s / 86400 | 0}d`
})

const icons = {
  folder: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M3 7h7l2 2h9v10H3V7Z"/></svg>',
  rocket: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M12 2c3 4 4 8 4 12H8c0-4 1-8 4-12Z"/><circle cx="12" cy="9" r="2"/><path d="M8 14c-3 1-5 4-5 6 2 0 5-2 6-4M16 14c3 1 5 4 5 6-2 0-5-2-6-4"/></svg>',
  layers: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="m12 3 9 5-9 5-9-5 9-5ZM3 12l9 5 9-5M3 16l9 5 9-5"/></svg>',
  harddrive: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M6 15h12M7 9h.01M10 9h.01" stroke-width="2"/></svg>',
  terminal: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3m5 0h4"/></svg>',
  chart: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M4 20V4M4 20h16M7 15l3-4 3 2 5-7"/></svg>',
  users: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><circle cx="9" cy="8" r="3"/><path d="M3 20c.7-4 2.6-6 6-6s5.3 2 6 6M16 5.5a3 3 0 0 1 0 5.5M17 14c2.2.4 3.5 2.2 4 5"/></svg>',
  globe: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 3.5 5.5 3.5 9S14.5 18.5 12 21c-2.5-2.5-3.5-6-3.5-9S9.5 5.5 12 3Z"/></svg>',
}

const navCards = computed(() => [
  { to: '/projects', title: 'Projects', desc: 'Organize applications by product or team', icon: icons.folder, count: String(ov.value.projects ?? projects.value.length) },
  { to: '/apps', title: 'Applications', desc: 'Deploy and operate your services', icon: icons.rocket, count: String(ov.value.projects ?? '') },
  { to: '/deployments', title: 'Deployments', desc: 'Releases, rollbacks and build history', icon: icons.rocket, count: deployCount.value === '—' ? '' : deployCount.value },
  { to: '/nodes', title: 'Nodes & Fleet', desc: 'MicroVM fleet and placement', icon: icons.harddrive, count: ov.value.total_vms != null ? String(ov.value.total_vms) : '' },
  { to: '/domains', title: 'Domains', desc: 'Custom domains and DNS', icon: icons.globe, count: '' },
  { to: '/logs', title: 'Logs', desc: 'Application and system logs', icon: icons.terminal, count: '' },
  { to: '/metrics', title: 'Metrics', desc: 'CPU, memory, traffic and latency', icon: icons.chart, count: '' },
  { to: '/team', title: 'Team', desc: 'Members, roles and access', icon: icons.users, count: '' },
])

async function load() {
  loading.value = true
  err.value = ''
  const failed = []
  // Each source is independent — one failing section must not blank the page.
  await Promise.all([
    get('/overview').then((d) => { ov.value = d || {} }).catch(() => failed.push('overview')),
    get('/projects').then((d) => { projects.value = Array.isArray(d) ? d : [] }).catch(() => failed.push('projects')),
    get('/servers').then((d) => { servers.value = Array.isArray(d) ? d : [] }).catch(() => failed.push('servers')),
    get('/orgs/events').then((d) => { events.value = (d && Array.isArray(d.events)) ? d.events : [] }).catch(() => failed.push('events')),
    get('/orgs/default').then((d) => { orgName.value = d?.name || '' }).catch(() => {}),
  ])
  // Deployment totals come per-project; sum what we can, never invent a number.
  let total = 0, ok = true
  for (const p of projects.value.slice(0, 50)) {
    try {
      const ds = await get(`/projects/${encodeURIComponent(p.id)}/deployments?limit=200`)
      total += Array.isArray(ds) ? ds.length : 0
    } catch (_) { ok = false }
  }
  deployCount.value = ok || projects.value.length === 0 ? String(total) : `${total}+`
  if (failed.length) {
    if (!ov.value.version && !projects.value.length) err.value = 'Could not reach the Porter API.'
    else err.value = `Some sections failed to load: ${failed.join(', ')}.`
  }
  loading.value = false
}

onMounted(load)
</script>

<style scoped>
.intro{display:flex;justify-content:space-between;align-items:center;gap:20px;padding:22px 26px;margin-bottom:18px}
.intro h2{font-size:20px;font-weight:650;letter-spacing:-.02em;margin:0}
.intro p{font-size:12.5px;color:var(--ink2);margin-top:4px}
.context{display:flex;align-items:center;gap:8px;padding:8px 13px;border:1px solid var(--hair);border-radius:9px;background:rgba(255,255,255,.8);font-size:11.5px;white-space:nowrap}
.context strong{font-size:12px}
.stats{display:grid;grid-template-columns:repeat(5,1fr);gap:12px;margin-bottom:24px}
.stat{padding:16px 18px}
.stat .label{font-size:11px;color:var(--ink2);font-weight:550}
.stat .value{font-size:24px;font-weight:650;margin-top:4px;letter-spacing:-.02em;font-variant-numeric:tabular-nums}
.stat .value.small{font-size:16px}
.stat .meta{font-size:10.5px;color:var(--ink2);margin-top:5px}
.meta.good{color:var(--green)}
.meta.warn{color:var(--orange)}
.meta.bad{color:var(--red)}
.section{margin-top:22px}
.section-title{display:flex;align-items:center;justify-content:space-between;margin:0 3px 10px}
.section-title h3{font-size:13.5px;font-weight:650;letter-spacing:-.01em;margin:0}
.section-title span{font-size:11px;color:var(--ink2)}
.card-grid{display:grid;grid-template-columns:repeat(4,1fr);gap:12px}
.nav-card{padding:18px;min-height:128px;position:relative;color:inherit;display:block;transition:transform .14s,box-shadow .14s,border-color .14s}
.nav-card:hover{transform:translateY(-2px);box-shadow:0 8px 24px rgba(0,0,0,.06);border-color:rgba(0,113,227,.28)}
.nav-icon{width:36px;height:36px;border-radius:10px;display:grid;place-items:center;background:rgba(0,113,227,.08);color:var(--blue);margin-bottom:12px}
.nav-icon :deep(svg){width:18px;height:18px}
.nav-card h4{font-size:13px;font-weight:600;margin:0}
.nav-card p{font-size:11px;line-height:1.45;color:var(--ink2);margin-top:4px}
.count{position:absolute;top:18px;right:18px;color:var(--ink2);font-size:11px;font-weight:550;background:rgba(0,0,0,.04);padding:2px 7px;border-radius:6px;font-variant-numeric:tabular-nums}
.errbar{display:flex;align-items:center;gap:10px;background:rgba(255,69,58,.08);border:1px solid rgba(255,69,58,.25);color:#C22A22;border-radius:var(--r2);padding:9px 12px;font-size:12.5px;margin-bottom:14px}
.errbar button{padding:4px 10px;font-size:12px}
@media(max-width:1100px){.stats{grid-template-columns:repeat(3,1fr)}.card-grid{grid-template-columns:repeat(3,1fr)}}
@media(max-width:760px){.stats{grid-template-columns:1fr 1fr}.card-grid{grid-template-columns:1fr 1fr}.intro{flex-direction:column;align-items:flex-start}}
</style>
