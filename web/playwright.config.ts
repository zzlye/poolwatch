import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry'
  },
  // 优先复用本机 Chrome，避免测试环境重复下载浏览器。
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'], channel: 'chrome' } }],
  webServer: {
    // 端到端测试使用前端内置模拟数据，确保无需依赖外部服务即可稳定执行。
    command: 'npm run build && npm run preview -- --host 127.0.0.1 --port 4173',
    cwd: process.cwd(),
    env: { VITE_USE_MOCKS: 'true', POOLWATCH_E2E_BUILD: 'true' },
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI
  }
})
