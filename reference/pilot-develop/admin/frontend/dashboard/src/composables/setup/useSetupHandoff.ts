import { ref } from 'vue'

const awaitingTerminal = ref(false)

export const useSetupHandoff = () => {
  return { awaitingTerminal }
}
