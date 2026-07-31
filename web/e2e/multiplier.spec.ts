import { expect, test, type Page } from '@playwright/test'

async function expectNoHorizontalScroll(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
}

test('渠道详情可配置倍率，折叠总览只显示已选分组和完整模型价格', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/targets/new-api-main#multiplier-settings')

  await expect(page.getByRole('heading', { name: '分组倍率监控' })).toBeVisible()
  await page.getByRole('button', { name: '自动检测分组' }).click()
  await page.getByRole('checkbox', { name: '监控 会员分组' }).check()
  await page.getByRole('button', { name: '保存监控' }).click()
  await expect(page.getByText(/已监控 2 个分组/)).toBeVisible()

  await page.getByRole('link', { name: '倍率' }).first().click()
  const channel = page.locator('details').filter({ hasText: '主站额度' })
  await channel.locator('summary').click()
  await expect(channel.getByRole('button', { name: /默认分组/ })).toBeVisible()
  await expect(channel.getByRole('button', { name: /会员分组/ })).toBeVisible()
  await expect(channel.getByText('高性能分组')).toHaveCount(0)
  await channel.getByRole('button', { name: /会员分组/ }).click()
  await expect(channel.getByText('gpt-4.1-long').first()).toBeVisible()
  await expect(channel.getByText('长上下文（≥ 200001 tokens）').first()).toBeVisible()
  await channel.getByPlaceholder('搜索模型或计费方式').fill('阶梯计费')
  await expect(channel.getByText('gpt-4.1-long').first()).toBeVisible()
  await expect(channel.getByText('gpt-4.1', { exact: true })).toHaveCount(0)
  await expectNoHorizontalScroll(page)
})

test('390×844：渠道折叠卡与价格卡片不产生横向滚动', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/multipliers?target=new-api-main')

  const channel = page.locator('details').filter({ hasText: '主站额度' })
  await expect(channel).toHaveAttribute('open', '')
  await expect(channel.getByText('倍率状态')).toBeVisible()
  await expect(channel.getByText('已选分组')).toBeVisible()
  await expect(channel.getByText('最近检测', { exact: true })).toBeVisible()
  await expect(channel.getByRole('button', { name: /默认分组/ })).toBeVisible()
  await expect(channel.getByText('gpt-4.1').first()).toBeVisible()
  await expect(channel.locator('.group-price-table tbody tr').first()).toBeVisible()
  await expectNoHorizontalScroll(page)

  const layout = await page.evaluate(() => {
    const summary = document.querySelector<HTMLElement>('.multiplier-channel-details summary')?.getBoundingClientRect()
    const navigation = document.querySelector<HTMLElement>('.bottom-nav')?.getBoundingClientRect()
    return { summaryWidth: summary?.width, viewportWidth: window.innerWidth, navigationTop: navigation?.top }
  })
  expect(layout.summaryWidth).toBeLessThanOrEqual(layout.viewportWidth ?? 0)
  expect(layout.navigationTop).toBeGreaterThan(700)
})

test('820×900：折叠总览和桌面价格表不会裁掉内容', async ({ page }) => {
  await page.setViewportSize({ width: 820, height: 900 })
  await page.goto('/multipliers?target=new-api-main')

  await expect(page.getByText('价格区间')).toBeVisible()
  await expect(page.getByText('元/百万 Token').first()).toBeVisible()
  await expectNoHorizontalScroll(page)
})
