import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 产物输出到 internal/server/webdist，供 Go go:embed 打进二进制。
const embedDir = fileURLToPath(new URL('../internal/server/webdist', import.meta.url))
// 演示站（vite build --mode demo）是部署到 Cloudflare Pages 的纯静态站点，接口由浏览器里的模拟后端应答
// （src/demo），产物单独放在 web/dist-demo，不进二进制。
const demoDir = fileURLToPath(new URL('./dist-demo', import.meta.url))

// emptyOutDir 会清掉入库的 .gitkeep；构建结束后补回，
// 保证未构建前端时 go:embed 仍有文件可嵌、git 工作区保持干净。
function keepEmbedDir(): Plugin {
  return {
    name: 'keep-embed-dir',
    apply: 'build',
    closeBundle() {
      writeFileSync(join(embedDir, '.gitkeep'), '')
    },
  }
}

export default defineConfig(({ mode }) => ({
  plugins: [vue(), tailwindcss(), ...(mode === 'demo' ? [] : [keepEmbedDir()])],
  // 路由是 History 模式，资源要用绝对路径：相对路径在 /keys/123 这类多级地址下会解析到 /keys/assets/…。
  base: '/',
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: mode === 'demo' ? demoDir : embedDir,
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      // 开发态把 API 代理到 Go 后端，前后端同源，免 CORS。
      '/api': 'http://127.0.0.1:8249',
      '/v1beta': 'http://127.0.0.1:8249',
      '/healthz': 'http://127.0.0.1:8249',
    },
  },
}))
