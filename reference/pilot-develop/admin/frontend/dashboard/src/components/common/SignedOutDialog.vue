<script setup lang="ts">
import { Dialog, type DialogAction } from 'frappe-ui'

const signInAgain = () => {
  const redirect = `${window.location.pathname}${window.location.search}`
  window.location.assign(`/login?redirect=${encodeURIComponent(redirect)}`)
}

const actions: DialogAction[] = [
  { label: 'Sign in again', variant: 'solid', onClick: signInAgain },
]
</script>

<template>
  <Dialog
    open
    size="sm"
    icon="lucide-lock"
    title="You were signed out"
    message="Sign in again to continue."
    :actions="actions"
    :dismissible="false"
    :show-close-button="false"
  />
</template>
