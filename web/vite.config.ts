import { readdirSync, rmSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'

const developmentPort = 32101
const outputDirectory = fileURLToPath(new URL('./dist', import.meta.url))

function cleanGeneratedAssets(): Plugin {
  return {
    name: 'stackpilot-clean-generated-assets',
    buildStart() {
      for (const entry of readdirSync(outputDirectory, { withFileTypes: true })) {
        if (entry.name !== 'embed-placeholder.txt') {
          rmSync(fileURLToPath(new URL(`./dist/${entry.name}`, import.meta.url)), {
            force: true,
            recursive: entry.isDirectory(),
          })
        }
      }
    },
  }
}

export default defineConfig({
  plugins: [cleanGeneratedAssets(), vue()],
  build: {
    emptyOutDir: false,
  },
  server: {
    host: '127.0.0.1',
    port: developmentPort,
    strictPort: true,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:32100',
      },
    },
  },
  preview: {
    host: '127.0.0.1',
    port: developmentPort,
    strictPort: true,
  },
})
