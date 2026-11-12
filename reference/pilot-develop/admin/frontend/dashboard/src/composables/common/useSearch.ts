import { onBeforeUnmount, onMounted, ref } from 'vue'

export const searchOpen = ref(false)

export const openSearch = () => {
  searchOpen.value = true
}

export const useSearchShortcut = () => {
  const onKeydown = (event: KeyboardEvent) => {
    if (event.key === 'k' && (event.metaKey || event.ctrlKey)) {
      event.preventDefault()
      searchOpen.value = true
    }
  }

  onMounted(() => window.addEventListener('keydown', onKeydown))
  onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
}
