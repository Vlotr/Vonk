import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

// The Go server. `make dev` runs it here; the Vite dev server forwards API calls to it.
const goServer = 'http://localhost:8000'

export default defineConfig({
  plugins: [react(), keepDistPlaceholder()],
  server: {
    proxy: {
      '/ui/api': goServer,
      '/health': goServer,
    },
  },
})

// Vite empties dist/ before every build. This puts back dist/.keep, the
// committed placeholder that lets `go build` embed dist/ before the dashboard
// has ever been built (see web/embed.go).
function keepDistPlaceholder(): Plugin {
  let outDir = ''
  return {
    name: 'keep-dist-placeholder',
    apply: 'build',
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir)
    },
    closeBundle() {
      writeFileSync(resolve(outDir, '.keep'), '')
    },
  }
}
