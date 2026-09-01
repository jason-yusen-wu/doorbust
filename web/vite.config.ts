import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    // The Go server sets Cache-Control: immutable on everything under
    // assets/ and no-store on index.html, which is only safe because Vite
    // fingerprints these filenames with a content hash.
    outDir: 'dist',
    assetsDir: 'assets',
  },
  server: {
    port: 5173,
    strictPort: true,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // Pinned so the suite does not depend on whether a developer happens to
    // have a web/.env, which is gitignored and absent in CI. Without this,
    // setting VITE_API_BASE_URL locally silently changes every request URL the
    // tests assert on — the suite passed in CI and failed on the machine that
    // had the file.
    //
    // Empty base URL is also the production configuration: the Go server
    // serves the bundle, so every request is a same-origin relative path.
    env: {
      VITE_API_BASE_URL: '',
      VITE_COGNITO_DOMAIN: '',
      VITE_COGNITO_CLIENT_ID: '',
      VITE_COGNITO_REDIRECT_URI: '',
      VITE_STRIPE_PUBLISHABLE_KEY: '',
      VITE_ENABLE_DEV_TOKEN_LOGIN: '',
    },
  },
})
