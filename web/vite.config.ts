import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发服务器代理到的 core 地址，可用环境变量 RSAGENT_API 覆盖（例如指向 --listen 127.0.0.1:7891 的实例）。
const api = process.env.RSAGENT_API || 'http://127.0.0.1:7890'

// 构建产物输出到 Go 包目录，由 go:embed 打进二进制。
// 不清空 outDir 以保留 .keep；build.ps1 / Makefile 会在构建前删除 assets/。
export default defineConfig({
  plugins: [vue()],
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
