import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

export default defineConfig({
  plugins: [
    vue(),
    Components({
      resolvers: [ElementPlusResolver()],
      dts: 'src/components.d.ts',
    }),
  ],
  server: { port: 5173, proxy: { '/api': 'http://127.0.0.1:8080', '/ws': 'ws://127.0.0.1:8080' } },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (
            id.includes('/node_modules/@vue/') ||
            id.includes('/node_modules/vue-router/') ||
            id.includes('/node_modules/pinia/') ||
            id.includes('/node_modules/vue-i18n/')
          ) {
            return 'vue-vendor'
          }
        },
      },
    },
  },
test: { environment: 'jsdom', css: true, server: { deps: { inline: ['element-plus'] } } }
})
