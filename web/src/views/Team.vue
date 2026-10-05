<template>
  <div>
    <div class="row" style="justify-content:space-between;margin-bottom:14px">
      <h1 style="margin:0">Team &amp; Access</h1>
      <button @click="open = !open">{{ open ? 'Cancel' : 'Add member' }}</button>
    </div>
    <div class="card" v-if="open" style="margin-bottom:14px">
      <div class="grid" style="grid-template-columns:1fr 1fr auto;align-items:end">
        <div><label class="muted">Username</label><input v-model="form.username"></div>
        <div><label class="muted">Password (new account)</label><input v-model="form.password" type="password"></div>
        <button :disabled="!form.username || !form.password" @click="addMember">Add</button>
      </div>
      <div class="muted" style="margin-top:8px">{{ msg }}</div>
    </div>
    <h2>Org members</h2>
    <div class="card">
      <table v-if="members.length">
        <thead><tr><th>User</th><th>Role</th></tr></thead>
        <tbody>
          <tr v-for="m in members" :key="typeof m === 'string' ? m : (m.username || m.user_id)">
            <td class="mono">{{ typeof m === 'string' ? m : (m.username || m.user_id) }}</td>
            <td><span class="pill info">{{ typeof m === 'string' ? 'member' : (m.role || 'member') }}</span></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">Loading members…</div>
    </div>
    <h2>Scoped role assignments <span class="muted" style="font-weight:400">(RBAC: union at scope + ancestors, deny wins)</span></h2>
    <div class="card">
      <table v-if="assignments.length">
        <thead><tr><th>Principal</th><th>Role</th><th>Scope</th><th></th></tr></thead>
        <tbody>
          <tr v-for="(a, i) in assignments" :key="i">
            <td class="mono">{{ a.principal_id || a.PrincipalID || a.principal || a.Principal }}</td>
            <td><span class="pill info">{{ a.role_id || a.RoleID || a.role || a.Role }}</span></td>
            <td class="mono muted">{{ a.scope_type || a.ScopeType }} {{ a.scope_id || a.ScopeID }}</td>
            <td><button class="danger" @click="revoke(a)">Revoke</button></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No scoped assignments.</div>
      <div class="grid" style="grid-template-columns:1fr 1fr 1fr 1fr auto;align-items:end;margin-top:12px">
        <div><label class="muted">Principal (username)</label><input v-model="assign.principal_id"></div>
        <div><label class="muted">Role</label><input v-model="assign.role_id" placeholder="admin"></div>
        <div><label class="muted">Scope type</label>
          <select v-model="assign.scope_type"><option>platform</option><option>org</option><option>project</option></select></div>
        <div><label class="muted">Scope id</label><input v-model="assign.scope_id" placeholder="org uuid"></div>
        <button @click="assignRole">Assign</button>
      </div>
      <div class="muted" style="margin-top:8px">{{ msg }}</div>
    </div>
    <h2>Audit</h2>
    <div class="card">
      <table v-if="audit.length">
        <thead><tr><th>When</th><th>Actor</th><th>Action</th><th>Outcome</th></tr></thead>
        <tbody>
          <tr v-for="e in audit" :key="e.id">
            <td class="muted">{{ ago(e.at) }}</td>
            <td class="mono">{{ e.actor_id }}</td>
            <td class="mono">{{ e.action }}</td>
            <td><span class="pill" :class="e.outcome === 'allowed' ? 'ok' : 'bad'">{{ e.outcome }}</span></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">No audit events yet.</div>
    </div>
  </div>
</template>
<script setup>
import { ref, onMounted } from 'vue'
import { get, post, del, ago } from '../lib/api.js'
const members = ref([]), assignments = ref([]), audit = ref([]), open = ref(false), msg = ref('')
const form = ref({ username: '', password: '' })
const assign = ref({ principal_type: 'user', principal_id: '', role_id: 'admin', scope_type: 'org', scope_id: '' })
onMounted(load)
async function load() {
  try { members.value = await get('/orgs/members') } catch { members.value = [] }
  try { const d = await get('/rbac/assignments'); assignments.value = d.assignments || d || [] } catch { assignments.value = [] }
  try { const d = await get('/orgs/audit?limit=50'); audit.value = d.events || [] } catch { audit.value = [] }
  const o = await get('/orgs/default').catch(() => null)
  if (o && o.id && !assign.value.scope_id) assign.value.scope_id = o.id
}
async function addMember() {
  msg.value = ''
  try { await post('/orgs/members', form.value); form.value = { username: '', password: '' }; open.value = false; load() }
  catch (e) { msg.value = e.message }
}
async function assignRole() {
  msg.value = ''
  try { await post('/rbac/assignments', assign.value); load() }
  catch (e) { msg.value = e.message }
}
async function revoke(a) {
  try {
    await del(`/rbac/assignments?principal_type=${encodeURIComponent(a.principal_type || a.PrincipalType || 'user')}&principal_id=${encodeURIComponent(a.principal_id || a.PrincipalID || a.principal || a.Principal)}&role_id=${encodeURIComponent(a.role_id || a.RoleID || a.role || a.Role)}&scope_type=${encodeURIComponent(a.scope_type || a.ScopeType)}&scope_id=${encodeURIComponent(a.scope_id || a.ScopeID)}`)
    load()
  } catch (e) { msg.value = e.message }
}
</script>
