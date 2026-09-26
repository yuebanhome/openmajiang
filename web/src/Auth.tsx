import { useState } from 'react';
import type { FormEvent } from 'react';
import { errorMessage, post } from './api';
import { Button, FormField, Link, Notice } from './components';
import { navigate } from './hooks';
import type { User } from './types';
export function AuthPage({ route, onUser }: { route: string; onUser: (user?: User) => Promise<void> }) {
  const mode = route.split('?')[0].split('/').pop() ?? 'login';
  const params = new URLSearchParams(route.split('?')[1]);
  const [email, setEmail] = useState(''); const [password, setPassword] = useState(''); const [name, setName] = useState(''); const [token, setToken] = useState(params.get('token') ?? ''); const [terms, setTerms] = useState(false);
  const [busy, setBusy] = useState(false); const [error, setError] = useState(''); const [success, setSuccess] = useState('');
  const title = { login: '欢迎回到牌桌', register: '创建你的牌手账号', verify: '验证邮箱', 'verify-email': '验证邮箱', 'confirm-email': '确认更换邮箱', forgot: '找回密码', reset: '设置新密码' }[mode] ?? '登录';
  async function submit(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError(''); setSuccess('');
    try {
      if (mode === 'register') { await post('/v1/auth/register', { email, password, name, accept_terms: terms }); setPassword(''); setSuccess('如果此邮箱可用于注册，你将收到验证邮件。请在邮箱中打开链接完成验证，然后登录。'); }
      else if (mode === 'login') { const data = await post<{ user?: User }>('/v1/auth/login', { email, password }); setPassword(''); await onUser(data.user); navigate(params.get('return_to') ?? '/'); }
      else if (mode === 'forgot') { await post('/v1/auth/forgot-password', { email }); setSuccess('如果该邮箱对应可用账号，我们会发送重置链接。请检查邮箱与垃圾邮件文件夹。'); }
      else if (mode === 'reset') { await post('/v1/auth/reset-password', { token, password }); setPassword(''); setToken(''); setSuccess('密码已重置，旧设备会话已退出。请使用新密码登录。'); }
      else if (mode === 'confirm-email') { await post('/v1/me/email-change/confirm', { token }); setToken(''); await onUser(); setSuccess('邮箱已更换，请使用新邮箱重新登录。'); }
      else { await post('/v1/auth/verify-email', { token }); setToken(''); await onUser(); setSuccess('邮箱已验证，可以登录并加入牌桌了。'); }
    } catch (err) { setError(errorMessage(err)); } finally { setBusy(false); }
  }
  return <div className="auth-layout"><section className="auth-story"><div className="brand-mark large">發</div><span className="eyebrow light">OPENMAJIANG</span><h1>一张牌，<br/>一个好对手。</h1><p>和朋友打麻将，也和代码过过招。<br/>国标规则，开放牌桌，一起认真玩。</p><div className="story-note"><i/>不注册也能观看所有牌桌的弃牌</div></section><section className="auth-form"><Link href="/" className="back-link">← 返回大厅</Link><h2>{title}</h2><p className="muted">{mode === 'register' ? '验证邮箱后，即可创建房间、入座和接入 Bot。' : mode === 'login' ? '你的牌桌和对局记录都在这里。' : '保护你的账号，从一个可靠的邮箱开始。'}</p><Notice error>{error}</Notice><Notice>{success}</Notice><form onSubmit={submit}>
    {['login','register','forgot'].includes(mode) && <FormField label="邮箱"><input type="email" name="email" autoComplete="email" value={email} onChange={e => setEmail(e.target.value)} required placeholder="you@example.com" maxLength={254}/></FormField>}
    {mode === 'register' && <FormField label="展示名"><input value={name} onChange={e => setName(e.target.value)} required minLength={2} maxLength={32} autoComplete="nickname" placeholder="牌桌上的名字"/></FormField>}
    {['login','register','reset'].includes(mode) && <FormField label="密码" hint={mode !== 'login' ? '至少 12 个字符。建议使用密码管理器生成。' : undefined}><input type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} value={password} onChange={e => setPassword(e.target.value)} required minLength={mode === 'login' ? 1 : 12} maxLength={128}/></FormField>}
    {['verify','verify-email','confirm-email','reset'].includes(mode) && <FormField label="邮件中的验证令牌"><input value={token} onChange={e => setToken(e.target.value)} required autoComplete="off" spellCheck={false}/></FormField>}
    {mode === 'register' && <label className="check"><input type="checkbox" checked={terms} onChange={e => setTerms(e.target.checked)} required/>我同意<Link href="/terms">使用条款</Link>与<Link href="/privacy">隐私说明</Link></label>}
    <Button busy={busy} type="submit" className="wide">{mode === 'register' ? '注册并发送验证邮件' : mode === 'login' ? '登录' : mode === 'forgot' ? '发送重置邮件' : mode === 'reset' ? '更新密码' : '验证邮箱'}</Button>
  </form><div className="auth-links">{mode === 'login' ? <><Link href={`/auth/register${params.has('return_to') ? `?return_to=${encodeURIComponent(params.get('return_to')!)}` : ''}`}>创建账号</Link><Link href="/auth/forgot">忘记密码？</Link></> : <Link href="/auth/login">已有账号？去登录 →</Link>}</div></section></div>;
}
