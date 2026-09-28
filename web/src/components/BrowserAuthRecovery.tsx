import { Globe } from 'lucide-react'
import { Link } from 'react-router-dom'
import type { TargetKind } from '../types'

// 只识别当前适配器明确报告的浏览器验证，其他登录错误保留原处理方式。
export function requiresBrowserAuthorization(kind: TargetKind, message?: string): boolean {
  return kind === 'new_api' && Boolean(message?.includes('浏览器验证'))
}

export function BrowserAuthRecovery({ baseUrl, href, onRecover }: { baseUrl: string; href?: string; onRecover?: () => void }) {
  let loginUrl = ''
  try {
    const parsed = new URL('/login', baseUrl)
    if (parsed.protocol === 'https:' || parsed.protocol === 'http:') loginUrl = parsed.toString()
  } catch {
    // 用户尚未填完有效地址时，不生成站外登录链接。
  }
  return (
    <section className="browser-auth-panel" aria-label="浏览器验证恢复">
      <div className="browser-auth-heading"><div><strong>开启网页验证后仍可监控</strong><small>请先在渠道站点正常登录并完成验证，再导入有效登录状态；也可选择管理访问令牌。</small></div></div>
      <p>服务器会复用有效凭据查询余额，不会每次检测都重新密码登录。登录状态失效后需要重新授权。</p>
      <div className="browser-auth-actions">
        {href ? <Link className="button primary" to={href}><Globe size={18} aria-hidden="true" />网页登录恢复检测</Link> : null}
        {onRecover ? <button className="button primary" type="button" onClick={onRecover}><Globe size={18} aria-hidden="true" />网页登录恢复检测</button> : null}
        {!href && !onRecover && loginUrl ? <a className="button secondary" href={loginUrl} target="_blank" rel="noopener noreferrer">打开渠道登录页</a> : null}
      </div>
      {!href && !onRecover ? <p>电脑端完成登录后使用浏览器助手读取当前地址；安卓端使用下方的授权窗口。连接测试成功后再保存。</p> : null}
    </section>
  )
}
