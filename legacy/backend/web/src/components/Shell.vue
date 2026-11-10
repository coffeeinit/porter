<template>
  <div class="app-shell">
    <header class="topbar">
      <button class="mobile-nav-toggle" type="button" aria-label="Toggle navigation" @click="mobileOpen = !mobileOpen">☰</button>
      <router-link to="/home" class="brand"><span class="brand-mark">P</span><span>Porter</span></router-link>
      <span class="top-context">Control plane</span>
      <div class="top-actions">
        <router-link to="/observability" class="top-live"><span class="status-dot"></span>Signals</router-link>
        <span class="workspace-chip"><span class="workspace-avatar">{{ initials(operatorName) }}</span>{{ operatorName }}</span>
        <button class="ghost logout" @click="logout">Log out</button>
      </div>
    </header>

    <div class="app-frame">
      <div v-if="mobileOpen" class="nav-scrim" @click="mobileOpen = false"></div>
      <aside class="sidebar" :class="{ 'mobile-open': mobileOpen }">
        <div class="workspace-label">WORKSPACE</div>
        <nav v-for="group in navGroups" :key="group.title" class="nav-group" :aria-label="group.title">
          <div class="nav-heading">{{ group.title }}</div>
          <router-link v-for="item in group.items" :key="item.to" :to="item.to" class="nav-link" @click="mobileOpen = false">
            <span class="nav-icon">{{ item.icon }}</span><span>{{ item.label }}</span><span v-if="item.badge" class="nav-badge">{{ item.badge }}</span>
          </router-link>
        </nav>
        <div class="sidebar-footer">
          <div class="runtime-mark"><span class="status-dot"></span><div><strong>Firecracker / KVM</strong><small>Workload runtime</small></div></div>
          <p>Build inputs are separate from production runtime.</p>
        </div>
      </aside>

      <main class="main-content"><router-view /></main>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { logout } from '../lib/api.js'

const mobileOpen = ref(false)
const operatorName = 'Operator'
const navGroups = [
  { title: 'Workspace', items: [
    { to: '/home', label: 'Overview', icon: '⌂' },
    { to: '/nodes', label: 'Fleet', icon: '⌘' },
  ] },
  { title: 'Build', items: [
    { to: '/projects', label: 'Projects', icon: '▦' },
    { to: '/services', label: 'Services', icon: '◈' },
    { to: '/releases', label: 'Releases', icon: '↗' },
    { to: '/instances', label: 'Instances', icon: '▣' },
    { to: '/images', label: 'Runtime images', icon: '▤' },
  ] },
  { title: 'Observe', items: [
    { to: '/observability', label: 'Observability', icon: '⌁' },
  ] },
  { title: 'Operate', items: [
    { to: '/nodes', label: 'Fleet', icon: '⌘' },
    { to: '/tasks', label: 'Operations', icon: '◷' },
  ] },
  { title: 'Platform', items: [
    { to: '/domains', label: 'Domains', icon: '◎' },
    { to: '/networking', label: 'Networking', icon: '↔' },
    { to: '/storage', label: 'Storage', icon: '▱' },
    { to: '/backups', label: 'Backups', icon: '⤓' },
    { to: '/variables', label: 'Configuration', icon: '{ }' },
    { to: '/secrets', label: 'Secrets', icon: '⌑' },
    { to: '/cron', label: 'Scheduled jobs', icon: '◴' },
  ] },
]

function initials(value) { return String(value || 'W').trim().slice(0, 1).toUpperCase() }
</script>
