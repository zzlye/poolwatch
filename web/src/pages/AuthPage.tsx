import { useState, type FormEvent } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Eye, EyeOff, KeyRound, LoaderCircle, ShieldCheck } from 'lucide-react'
import { api } from '../api/client'
import { InlineMessage } from '../components/Common'

interface AuthPageProps {
  initialized: boolean
  productName: string
  onAuthenticated: () => Promise<void>
}

export default function AuthPage({ initialized, productName, onAuthenticated }: AuthPageProps) {
  const [initializationToken, setInitializationToken] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [secondFactor, setSecondFactor] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [validationError, setValidationError] = useState('')

  const mutation = useMutation({
    mutationFn: () => initialized
      ? api.login({ username, password, secondFactor: secondFactor || undefined })
      : api.setup({ initializationToken, username, password }),
    onSuccess: onAuthenticated
  })

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault()
    setValidationError('')
    if (!username.trim() || !password) {
      setValidationError('请填写管理员账号和密码。')
      return
    }
    if (!initialized && !initializationToken.trim()) {
      setValidationError('请填写部署时设置的初始化口令。')
      return
    }
    if (!initialized && password.length < 10) {
      setValidationError('管理员密码至少需要 10 个字符。')
      return
    }
    if (!initialized && password !== confirmPassword) {
      setValidationError('两次输入的密码不一致。')
      return
    }
    mutation.mutate()
  }

  return (
    <main className="auth-layout">
      <section className="auth-panel" aria-labelledby="auth-title">
        <div className="auth-brand">
          <span className="brand-mark large"><ShieldCheck aria-hidden="true" /></span>
          <div><strong>{productName}</strong><span>渠道与号池状态中心</span></div>
        </div>
        <div className="auth-heading">
          <span className="eyebrow"><KeyRound aria-hidden="true" size={16} />{initialized ? '管理员登录' : '首次设置'}</span>
          <h1 id="auth-title">{initialized ? '欢迎回来' : '创建唯一管理员'}</h1>
          <p>{initialized ? '登录后查看所有渠道余额与账号告警。' : '使用服务器初始化口令完成首次设置，此入口完成后会永久关闭。'}</p>
        </div>

        <form className="form-stack" onSubmit={handleSubmit} noValidate>
          {!initialized ? (
            <label className="field">
              <span>初始化口令 <b aria-hidden="true">*</b></span>
              <input type="password" value={initializationToken} onChange={(event) => setInitializationToken(event.target.value)} autoComplete="one-time-code" />
              <small>来自服务器部署配置，不是管理员登录密码。</small>
            </label>
          ) : null}
          <label className="field">
            <span>管理员账号 <b aria-hidden="true">*</b></span>
            <input value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" />
          </label>
          <label className="field">
            <span>管理员密码 <b aria-hidden="true">*</b></span>
            <span className="input-with-action">
              <input type={showPassword ? 'text' : 'password'} value={password} onChange={(event) => setPassword(event.target.value)} autoComplete={initialized ? 'current-password' : 'new-password'} />
              <button className="input-icon-button" type="button" aria-label={showPassword ? '隐藏密码' : '显示密码'} onClick={() => setShowPassword((value) => !value)}>
                {showPassword ? <EyeOff aria-hidden="true" size={19} /> : <Eye aria-hidden="true" size={19} />}
              </button>
            </span>
            {!initialized ? <small>至少 10 个字符，建议使用密码管理器生成。</small> : null}
          </label>
          {!initialized ? (
            <label className="field">
              <span>确认密码 <b aria-hidden="true">*</b></span>
              <input type={showPassword ? 'text' : 'password'} value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} autoComplete="new-password" />
            </label>
          ) : (
            <label className="field">
              <span>二步验证码或恢复码 <em>可选</em></span>
              <input value={secondFactor} onChange={(event) => setSecondFactor(event.target.value)} autoComplete="one-time-code" autoCapitalize="characters" />
            </label>
          )}

          {validationError ? <InlineMessage tone="danger">{validationError}</InlineMessage> : null}
          {mutation.error ? <InlineMessage tone="danger">{mutation.error.message}</InlineMessage> : null}
          <button className="button primary full" type="submit" disabled={mutation.isPending}>
            {mutation.isPending ? <LoaderCircle className="spin" aria-hidden="true" size={18} /> : <KeyRound aria-hidden="true" size={18} />}
            {mutation.isPending ? '正在验证' : initialized ? '登录' : '完成设置'}
          </button>
        </form>
      </section>
    </main>
  )
}
