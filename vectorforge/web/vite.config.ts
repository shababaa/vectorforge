import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const backend = process.env.VECTORFORGE_URL ?? 'http://127.0.0.1:8080'

// In development the page is served from Vite's origin. The API rejects
// cross-origin mutations, so the dev proxy presents the backend's own origin;
// production builds are served by the Go server from the same origin.
const proxy = { target: backend, changeOrigin: true, headers: { origin: backend } }

export default defineConfig({
  plugins: [react()],
  server: { host: '127.0.0.1', proxy: { '/api': proxy, '/health': proxy } },
  build: { outDir: 'dist', emptyOutDir: true },
})
