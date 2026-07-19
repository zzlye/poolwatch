import { expect, test, type Page } from '@playwright/test'

async function expectNoHorizontalScroll(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
}

test('1440×900：桌面页面无横向滚动且可通过键盘跳到主要内容', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/')

  await expect(page.getByRole('heading', { name: '监控总览' })).toBeVisible()
  await expectNoHorizontalScroll(page)

  await page.keyboard.press('Tab')
  await expect(page.locator('.skip-link')).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main-content')).toBeFocused()
})

test('390×844：手机页面无横向滚动、底部导航不遮挡设置页内容', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/')

  await expect(page.getByRole('navigation', { name: '手机主导航' })).toBeVisible()
  await expectNoHorizontalScroll(page)

  await page.getByRole('link', { name: '设置' }).last().focus()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/settings$/)
  await expect(page.getByRole('heading', { name: '系统与安全' })).toBeVisible()
  await page.locator('.settings-footer').scrollIntoViewIfNeeded()
  await expectNoHorizontalScroll(page)

  const layout = await page.evaluate(() => {
    const footer = document.querySelector<HTMLElement>('.settings-footer')?.getBoundingClientRect()
    const navigation = document.querySelector<HTMLElement>('.bottom-nav')?.getBoundingClientRect()
    return { footerBottom: footer?.bottom, navigationTop: navigation?.top }
  })
  expect(layout.footerBottom).toBeLessThanOrEqual(layout.navigationTop ?? 0)
})

test('Service Worker：已注册并可检查更新', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { name: '监控总览' })).toBeVisible()

  const registration = await page.evaluate(async () => {
    const ready = await navigator.serviceWorker.ready
    await ready.update()
    return { active: Boolean(ready.active), scope: ready.scope }
  })

  expect(registration.active).toBe(true)
  expect(registration.scope).toContain('/')
})
