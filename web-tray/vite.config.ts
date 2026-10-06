import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

// The tray UI is served by the Go process from 127.0.0.1, so the base stays absolute
// and there is no dev proxy to configure: `npm run dev` points the front end at a
// running tray through TUNNELMESH_TRAY_ORIGIN instead.
export default defineConfig({
  plugins: [
    vue(),
    Components({
      resolvers: [ElementPlusResolver()],
      dts: 'src/components.d.ts',
    }),
  ],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('/node_modules/element-plus/')) return 'element-plus'
          if (id.includes('/node_modules/@vue/') || id.includes('/node_modules/vue/') ||
              id.includes('/node_modules/pinia/') || id.includes('/node_modules/vue-i18n/')) return 'vue-vendor'
        },
      },
    },
  },
  test: {
    environment: 'jsdom',
    css: true,
    server: { deps: { inline: ['element-plus'] } },
  },
})
