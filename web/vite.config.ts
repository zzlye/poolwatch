import { resolve } from 'node:path'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'
import { VitePWA } from 'vite-plugin-pwa'

const buildOutDir = process.env.POOLWATCH_E2E_BUILD === 'true'
  // 端到端测试的模拟构建放入系统临时目录，避免覆盖将要嵌入服务端的正式产物。
  ? resolve(process.env.TEMP || process.env.TMP || '/tmp', 'poolwatch-web-e2e')
  : 'dist'

export default defineConfig({
  build: { outDir: buildOutDir, emptyOutDir: true },
  plugins: [
    react(),
    VitePWA({
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.ts',
      registerType: 'prompt',
      injectRegister: false,
      includeAssets: ['icon.svg'],
      manifest: {
        name: '号池监控',
        short_name: '号池监控',
        description: '统一查看渠道余额、账号状态与告警',
        theme_color: '#147a55',
        background_color: '#f5f6f4',
        display: 'standalone',
        start_url: '/',
        scope: '/',
        lang: 'zh-CN',
        icons: [
          { src: '/icon-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
          { src: '/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any maskable' }
        ]
      },
      injectManifest: {
        globPatterns: ['**/*.{js,css,html,png,svg,webmanifest}'],
        maximumFileSizeToCacheInBytes: 3 * 1024 * 1024
      },
      devOptions: {
        enabled: true,
        type: 'module'
      }
    })
  ],
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      }
    }
  }
})
