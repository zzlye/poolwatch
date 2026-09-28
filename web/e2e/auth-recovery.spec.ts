import { expect, test } from '@playwright/test'

// 在独立模拟构建中验证授权恢复，不访问真实渠道或使用实际凭据。
for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
  test(`浏览器验证恢复：${viewport.width}×${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await page.goto('/targets/new-api-main/edit?auth=browser')
    await expect(page.getByRole('heading', { name: '登录与认证' })).toBeVisible()
    await expect(page.getByRole('radio', { name: /网页授权/ })).toBeChecked()
    await expect(page.getByRole('link', { name: '打开渠道登录页' })).toHaveAttribute('href', 'https://api.example.com/login')
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.getByRole('button', { name: '下一步' }).click()
    await expect(page.getByRole('alert')).toContainText('请先完成当前站点的网页授权')
    await page.getByRole('link', { name: '打开渠道登录页' }).focus()
    await expect(page.getByRole('link', { name: '打开渠道登录页' })).toBeFocused()
    await page.screenshot({ path: `D:/tmp/poolwatch-turnstile-20260928/recovery-${viewport.width}.png`, fullPage: true })
  })
}
