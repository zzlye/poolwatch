import { expect, test, type Page } from '@playwright/test'

async function expectNoHorizontalScroll(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
}

test('号池账号可按邮箱、类型和状态筛选并分页', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/targets/chat-pool')

  await expect(page.getByRole('heading', { name: '号池账号状态' })).toBeVisible()
  await expect(page.getByText('显示第 1–10 条，共 23 条')).toBeVisible()
  await expect(page.getByText('demo11***@example.com')).toHaveCount(0)

  await page.getByRole('button', { name: '下一页' }).click()
  await expect(page.getByText('demo11***@example.com')).toBeVisible()
  await page.getByRole('combobox', { name: '账号类型' }).selectOption('plus')
  await page.getByRole('combobox', { name: '账号状态' }).selectOption('warning')
  await expect(page.getByText(/显示第 1–2 条，共 2 条/)).toBeVisible()
  await expect(page.locator('.status-pill').filter({ hasText: '限流' })).toHaveCount(2)

  await page.getByRole('searchbox', { name: '搜索账号邮箱' }).fill('demo14')
  await expect(page.getByText(/显示第 1–1 条，共 1 条/)).toBeVisible()
  await expect(page.getByText('demo14***@example.com')).toBeVisible()
  await expectNoHorizontalScroll(page)
})

test('390×844：账号表格切换为无横向滚动的手机列表', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/targets/chat-pool')

  await expect(page.getByRole('heading', { name: '号池账号状态' })).toBeVisible()
  await page.getByRole('heading', { name: '号池账号状态' }).scrollIntoViewIfNeeded()
  await expect(page.getByRole('columnheader', { name: '账号' })).toBeAttached()
  await expect(page.getByRole('combobox', { name: '每页数量' })).toHaveValue('10')
  await expectNoHorizontalScroll(page)

  await page.getByRole('combobox', { name: '每页数量' }).selectOption('20')
  await expect(page.getByText('显示第 1–20 条，共 23 条')).toBeVisible()
  await expectNoHorizontalScroll(page)
})
