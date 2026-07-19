import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: true,
    // 端到端用例由 Playwright 单独执行，避免被 Vitest 载入。
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    exclude: ['e2e/**']
  }
})
