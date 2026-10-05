<template>
  <div style="display:grid;place-items:center;min-height:100vh">
    <form class="card" style="width:360px;padding:28px" @submit.prevent="submit">
      <div style="font-size:22px;font-weight:700;letter-spacing:-.02em">Porter</div>
      <div class="muted" style="margin-bottom:16px">MicroVM compute, simplified.</div>
      <input v-model="username" placeholder="Username" autocomplete="username" required>
      <input v-model="password" type="password" placeholder="Password" autocomplete="current-password" required>
      <button type="submit" style="width:100%;margin-top:6px">Log in</button>
      <div class="muted" style="margin-top:10px;min-height:18px;color:var(--red)">{{ error }}</div>
    </form>
  </div>
</template>
<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { login } from '../lib/api.js'
const router = useRouter()
const username = ref(''), password = ref(''), error = ref('')
async function submit() {
  error.value = ''
  try { await login(username.value.trim(), password.value); router.push('/home') }
  catch (e) { error.value = e.message }
}
</script>
