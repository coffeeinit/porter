<template>
  <div>
    <header class="top">
      <span class="brand">Porter</span>
      <span class="muted">{{ title }}</span>
      <span class="row" style="margin-left:auto">
        <span class="muted mono">{{ orgName }}</span>
        <button class="ghost" @click="logout">Log out</button>
      </span>
    </header>
    <div class="shell" style="display:grid;grid-template-columns:218px 1fr">
      <nav class="side" style="border-right:1px solid var(--hair);padding:14px 10px">
        <router-link v-for="n in nav" :key="n.to" :to="n.to">{{ n.label }}</router-link>
      </nav>
      <main style="padding:20px 24px;max-width:1180px"><router-view /></main>
    </div>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { logout, get, org } from '../lib/api.js'
const nav = [
  { to: '/home', label: 'Workspace' },
  { to: '/apps', label: 'Applications' },
  { to: '/images', label: 'Images' },
  { to: '/deployments', label: 'Deployments' },
  { to: '/domains', label: 'Domains & DNS' },
  { to: '/networking', label: 'Networking' },
  { to: '/nodes', label: 'Nodes & Fleet' },
  { to: '/storage', label: 'Storage' },
  { to: '/backups', label: 'Backups' },
  { to: '/logs', label: 'Logs' },
  { to: '/metrics', label: 'Metrics' },
  { to: '/tasks', label: 'Tasks' },
  { to: '/cron', label: 'Cron' },
  { to: '/secrets', label: 'Secrets' },
  { to: '/variables', label: 'Variables' },
  { to: '/team', label: 'Team & Access' },
  { to: '/billing', label: 'Billing & Usage' },
  { to: '/marketplace', label: 'Marketplace' },
]
const orgName = ref('')
onMounted(async () => {
  try { const o = await get('/orgs/default'); orgName.value = o.name || ''; if (o.id) org.id = o.id } catch (_) {}
})
</script>
