<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useColorScheme, FrappeUIProvider } from 'frappe-ui'

import ReconnectOverlay from '@/components/common/ReconnectOverlay.vue'
import SignedOutDialog from '@/components/common/SignedOutDialog.vue'
import MainLayout from '@/layouts/MainLayout.vue'

import { useSetupHandoff } from '@/composables/setup/useSetupHandoff'
import { useSignedOut } from '@/composables/auth/useSignedOut'

const route = useRoute()
const isFullScreen = computed(() => route.meta.fullScreen === true)

const { awaitingTerminal } = useSetupHandoff()
const { signedOut } = useSignedOut()

useColorScheme()
</script>

<template>
  <FrappeUIProvider>
    <ReconnectOverlay :paused="awaitingTerminal" />
    <SignedOutDialog v-if="signedOut" />

    <RouterView v-if="isFullScreen" />

    <MainLayout v-else>
      <RouterView />
    </MainLayout>
  </FrappeUIProvider>
</template>
