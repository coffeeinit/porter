import { ref } from 'vue'

// Standalone so api/client.js can report without importing useSession (which imports the client).
const signedOut = ref(false)

export const reportSignedOut = () => {
  signedOut.value = true
}

export const isSignedOut = () => {
  return signedOut.value
}

export const useSignedOut = () => {
  return { signedOut }
}
