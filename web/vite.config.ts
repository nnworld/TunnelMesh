import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: { port: 5173, proxy: { '/api': 'http://127.0.0.1:8080', '/ws': 'ws://127.0.0.1:8080' } },
  build: { outDir: 'dist', emptyOutDir: true },
  test: { environment: 'jsdom' }
})
