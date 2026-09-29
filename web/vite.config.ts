import { defineConfig } from 'vitest/config'
import { loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const base = env.VITE_BASE_PATH || '/'

  return {
    base,
    plugins: [react()],
    server: {
      proxy: {
        '/ws': { target: 'ws://localhost:8080', ws: true },
        '/chat/ws': { target: 'ws://localhost:8080', ws: true },
        '/api/file': { target: 'http://localhost:8080' },
        '/chat/api/file': { target: 'http://localhost:8080', rewrite: (path) => path.replace(/^\/chat/, '') },
        '/api/board': { target: 'http://localhost:8080' },
        '/chat/api/board': { target: 'http://localhost:8080', rewrite: (path) => path.replace(/^\/chat/, '') }
      }
    },
    build: { sourcemap: false },
    test: { environment: 'node' }
  }
})
