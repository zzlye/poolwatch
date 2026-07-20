import { describe, expect, it } from 'vitest'
import { mockRequest } from '../api/mock'
import type { DetectTargetResult, Target } from '../types'

describe('CLIProxyAPI 模拟数据', () => {
  it('提供带账号统计的渠道并支持地址识别', async () => {
    const targets = await mockRequest<Target[]>('/api/targets')
    const target = targets.find((item) => item.kind === 'cliproxyapi')

    expect(target).toBeDefined()
    const accountTotal = target?.metrics.find((item) => item.key === 'account_total')
    expect(accountTotal).toMatchObject({ value: '11' })
    expect(accountTotal?.threshold).toBeUndefined()
    expect(target?.metrics.find((item) => item.key === 'limited_accounts')).toMatchObject({ comparison: 'gte', threshold: '1' })
    expect(target?.accounts?.[0]).toEqual(expect.objectContaining({ provider: 'OpenAI', success: 100, fail: 0 }))

    const detected = await mockRequest<DetectTargetResult>('/api/targets/detect', {
      method: 'POST',
      body: JSON.stringify({ baseUrl: 'https://cli-proxy.example.com' })
    })
    expect(detected.kind).toBe('cliproxyapi')
  })
})
