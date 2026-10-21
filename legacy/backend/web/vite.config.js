import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The API the dev server proxies to. Overridable so the dashboard and the API
// can run side by side even when :8080 is already taken (for example by another
// Porter instance) — scripts/dev-up.sh sets these for you.
const target = process.env.PORTER_API_TARGET || 'http://localhost:8080'
const port = Number(process.env.PORTER_WEB_PORT || 5173)

export default defineConfig({
  plugins: [vue()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    host: process.env.PORTER_WEB_HOST || '127.0.0.1',
    port,
    strictPort: true,
    proxy: { '/api': { target, changeOrigin: true } },
    // The repo lives on a Windows drive mounted in WSL (/mnt/d), where
    // inotify does not fire — poll for changes so HMR actually triggers.
    watch: { usePolling: true, interval: 400 },
  },
})
