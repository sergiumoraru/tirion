import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/api': {
        // Match tirion serve's default IPv4 listener; localhost may resolve to ::1.
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
