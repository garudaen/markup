import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  build: {
    rollupOptions: {
      output: {
        // Split heavy render dependencies into cacheable vendor chunks.
        // mermaid is not listed here: it is lazy-loaded via dynamic import
        // (see markdown.ts) and gets its own chunks automatically.
        manualChunks: {
          katex: ['katex'],
          hljs: ['highlight.js'],
          markdown: ['markdown-it', '@traptitech/markdown-it-katex'],
        },
      },
    },
  },
})
