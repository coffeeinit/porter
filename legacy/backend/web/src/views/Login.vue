<template>
  <div style="display:grid;place-items:center;min-height:100vh;padding:20px">
    <form class="card" style="width:380px;max-width:100%;padding:28px" @submit.prevent="submit">
      <div style="font-size:22px;font-weight:700;letter-spacing:-.02em">Porter</div>
      <div class="muted" style="margin:4px 0 18px">Connect to the PostgreSQL-backed control plane.</div>
      <label class="muted" for="operator-token">Operator token</label>
      <input id="operator-token" v-model="token" type="password" autocomplete="current-password" placeholder="PORTER_ADMIN_TOKEN" required style="margin-top:6px">
      <p class="muted" style="font-size:12px;line-height:1.5;margin:10px 0">Use the token configured with the Go server. It stays in this browser tab and is sent only to the control-plane API.</p>
      <button type="submit" :disabled="connecting" style="width:100%;margin-top:6px">{{ connecting ? 'Connecting…' : 'Connect' }}</button>
      <div class="muted" role="alert" style="margin-top:10px;min-height:18px;color:var(--red)">{{ error }}</div>
    </form>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { login } from '../lib/api.js'

const router = useRouter()
const token = ref('')
const connecting = ref(false)
const error = ref('')

async function submit() {
  connecting.value = true
  error.value = ''
  try {
    await login(token.value)
    router.replace('/projects')
  } catch (cause) {
    error.value = cause.message || 'Could not authenticate with the control plane.'
  } finally {
    connecting.value = false
  }
}
</script>
