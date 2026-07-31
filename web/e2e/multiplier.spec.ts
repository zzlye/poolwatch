import { expect, test, type Page } from '@playwright/test'

async function expectNoHorizontalScroll(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
}

test('倍率页可检测分组、保存选择并只刷新当前渠道', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/multipliers?target=new-api-main')

  await expect(page.getByRole('heading', { name: '分组倍率' })).toBeVisible()
  await page.getByRole('button', { name: '自动检测分组' }).click()
  await expect(page.getByText('0.333333×')).toBeVisible()
  await page.getByRole('checkbox', { name: '监控 会员分组' }).check()
  await page.getByRole('button', { name: '保存监控' }).click()
  await expect(page.getByText(/已监控 2 个分组/)).toBeVisible()

  await page.getByLabel('已经添加的渠道').selectOption('sub2api-backup')
  await page.getByRole('button', { name: '自动检测分组' }).click()
  await page.getByRole('checkbox', { name: '监控 基础组' }).check()
  await page.getByRole('button', { name: '保存监控' }).click()
  await page.getByRole('button', { name: '刷新当前渠道' }).click()
  await expect(page.getByText('当前渠道倍率已经刷新。')).toBeVisible()
  await expectNoHorizontalScroll(page)
})

test('390×844：倍率表切换为手机卡片且保存栏不被底部导航遮挡', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/multipliers?target=new-api-main')

  await expect(page.getByRole('navigation', { name: '手机主导航' })).toBeVisible()
  await expect(page.getByRole('link', { name: '倍率' }).last()).toBeVisible()
  await page.getByRole('button', { name: '自动检测分组' }).click()
  await expect(page.getByText('当前倍率').first()).toBeVisible()
  await page.getByRole('checkbox', { name: '监控 会员分组' }).check()
  await page.getByRole('button', { name: '保存监控' }).scrollIntoViewIfNeeded()
  await expectNoHorizontalScroll(page)

  const layout = await page.evaluate(() => {
    const saveBar = document.querySelector<HTMLElement>('.multiplier-save-bar')?.getBoundingClientRect()
    const navigation = document.querySelector<HTMLElement>('.bottom-nav')?.getBoundingClientRect()
    const checkbox = document.querySelector<HTMLElement>('.multiplier-checkbox')?.getBoundingClientRect()
    return { saveBottom: saveBar?.bottom, navigationTop: navigation?.top, checkboxWidth: checkbox?.width, checkboxHeight: checkbox?.height }
  })
  expect(layout.saveBottom).toBeLessThanOrEqual(layout.navigationTop ?? 0)
  expect(layout.checkboxWidth).toBeGreaterThanOrEqual(44)
  expect(layout.checkboxHeight).toBeGreaterThanOrEqual(44)
})

test('820×900：窄桌面倍率列表不会裁掉状态内容', async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 900 })
  await page.goto('/multipliers?target=new-api-main')

  await page.getByRole('button', { name: '自动检测分组' }).click()
  await expect(page.getByText('当前倍率').first()).toBeVisible()
  await expect(page.getByText('未监控').first()).toBeVisible()
  await expectNoHorizontalScroll(page)
})
