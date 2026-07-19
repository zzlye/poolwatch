import { Bell, Gauge, LogOut, RadioTower, Settings, ShieldCheck } from 'lucide-react'
import { NavLink, Outlet } from 'react-router-dom'
import type { BootstrapState } from '../types'

const navigation = [
  { to: '/', label: '总览', icon: Gauge, end: true },
  { to: '/targets', label: '渠道', icon: RadioTower, end: false },
  { to: '/alerts', label: '告警', icon: Bell, end: false },
  { to: '/settings', label: '设置', icon: Settings, end: false }
]

export function AppShell({ bootstrap, onLogout }: { bootstrap: BootstrapState; onLogout: () => void }) {
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      <aside className="sidebar" aria-label="主导航">
        <div className="brand">
          <span className="brand-mark"><ShieldCheck aria-hidden="true" /></span>
          <span><strong>{bootstrap.productName}</strong><small>渠道与号池状态</small></span>
        </div>
        <nav className="side-nav">
          {navigation.map(({ to, label, icon: Icon, end }) => (
            <NavLink key={to} to={to} end={end} className={({ isActive }) => isActive ? 'nav-item active' : 'nav-item'}>
              <Icon aria-hidden="true" size={20} /><span>{label}</span>
            </NavLink>
          ))}
        </nav>
        <button className="nav-item logout-button" type="button" onClick={onLogout}>
          <LogOut aria-hidden="true" size={20} /><span>退出登录</span>
        </button>
      </aside>

      <div className="mobile-topbar">
        <span className="brand-mark"><ShieldCheck aria-hidden="true" /></span>
        <strong>{bootstrap.productName}</strong>
      </div>

      <main id="main-content" className="main-content" tabIndex={-1}>
        <Outlet />
      </main>

      <nav className="bottom-nav" aria-label="手机主导航">
        {navigation.map(({ to, label, icon: Icon, end }) => (
          <NavLink key={to} to={to} end={end} className={({ isActive }) => isActive ? 'bottom-nav-item active' : 'bottom-nav-item'}>
            <Icon aria-hidden="true" size={21} /><span>{label}</span>
          </NavLink>
        ))}
      </nav>
    </div>
  )
}
