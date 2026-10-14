import { rmSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发服务器代理到的 core 地址，可用环境变量 RSAGENT_API 覆盖（例如指向 --listen 127.0.0.1:7891 的实例）。
const api = process.env.RSAGENT_API || 'http://127.0.0.1:7890'

const outDir = resolve(__dirname, '../internal/web/dist')

// 构建前清空 assets/。outDir 不整体清空是为了保留 .keep；但带哈希的旧 assets 不删会一起被 go:embed
// 嵌进二进制，每次构建多出 1 MB 以上。
const cleanAssets: Plugin = {
  name: 'rsagent-clean-assets',
  apply: 'build',
  buildStart() {
    rmSync(resolve(outDir, 'assets'), { recursive: true, force: true })
  },
}

// 构建产物输出到 Go 包目录，由 go:embed 打进二进制。
export default defineConfig({
  plugins: [vue(), cleanAssets],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': api,
      '/ws': { target: api.replace(/^http/, 'ws'), ws: true },
    },
  },
})
