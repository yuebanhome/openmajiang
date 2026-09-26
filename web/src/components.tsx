import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { navigate } from './hooks';
export function Link({ href, children, className, ...rest }: { href: string; children: ReactNode; className?: string; title?: string; 'aria-label'?: string }) { return <a href={href} className={className} onClick={e => { if (!e.ctrlKey && !e.metaKey && !e.shiftKey && e.button === 0) { e.preventDefault(); navigate(href); } }} {...rest}>{children}</a>; }
export function Button({ children, busy, tone = 'primary', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { busy?: boolean; tone?: 'primary' | 'secondary' | 'quiet' | 'danger' }) { return <button {...props} disabled={props.disabled || busy} className={`button ${tone} ${props.className ?? ''}`} aria-busy={busy || undefined}>{busy && <span className="spinner" aria-hidden="true"/>}{children}</button>; }
export function Notice({ children, error = false }: { children?: ReactNode; error?: boolean }) { if (!children) return null; return <div className={`notice ${error ? 'error' : ''}`} role={error ? 'alert' : 'status'}>{children}</div>; }
export function Empty({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) { return <div className="empty"><span className="empty-tile" aria-hidden="true">發</span><h3>{title}</h3><p>{children}</p>{action}</div>; }
export function Loading({ text = '正在连接牌桌…' }: { text?: string }) { return <div className="loading" role="status"><span className="spinner"/>{text}</div>; }
export function PageHeading({ eyebrow, title, children, action }: { eyebrow?: string; title: string; children?: ReactNode; action?: ReactNode }) { return <div className="page-heading"><div>{eyebrow && <span className="eyebrow">{eyebrow}</span>}<h1>{title}</h1>{children && <p>{children}</p>}</div>{action}</div>; }
export const modeName: Record<string, string> = { human_only: '真人对局', mixed: '人机练习', bot_only: 'Bot 对弈' };
export const formatName: Record<string, string> = { practice_1: '单盘练习', practice_4: '四盘练习', standard_16: '标准 16 盘' };
export const statusName: Record<string, string> = { waiting: '等待入座', ready: '等待准备', playing: '正在对局', active: '正在对局', running: '正在对局', completed: '已完成', ended: '已结束', closed: '已关闭', aborted: '已中断', intermission: '盘间休息', early_terminated: '提前结束' };
export function Badge({ children, live = false }: { children: ReactNode; live?: boolean }) { return <span className={`badge ${live ? 'live' : ''}`}>{live && <i/>}{children}</span>; }
const honors: Record<string, string> = { '1z': '東', '2z': '南', '3z': '西', '4z': '北', '5z': '中', '6z': '發', '7z': '白' };
const flowerNames = ['春','夏','秋','冬','梅','蘭','竹','菊'];
export function tileLabel(kind: string) { if (honors[kind]) return honors[kind]; if (/^(?:[1-8]h|h[1-8])$/.test(kind)) return flowerNames[Number(kind.replace('h', '')) - 1]; if (/^[1-9][mps]$/.test(kind)) return `${kind[0]}${{ m: '萬', p: '筒', s: '索' }[kind[1]]}`; return '?'; }
export function TileFace({ kind, compact = false, selected = false, onClick, disabled = false, discarded = false }: { kind: string; compact?: boolean; selected?: boolean; onClick?: () => void; disabled?: boolean; discarded?: boolean }) {
  const color = kind.endsWith('m') || kind === '5z' ? 'red' : kind.endsWith('s') || kind === '6z' ? 'green' : 'blue';
  const content = <><span>{tileLabel(kind)}</span>{!honors[kind] && !kind.includes('h') && <small>{kind.toUpperCase()}</small>}</>;
  const cls = `tile ${color} ${compact ? 'compact' : ''} ${selected ? 'selected' : ''} ${discarded ? 'claimed' : ''}`;
  return onClick ? <button type="button" className={cls} onClick={onClick} disabled={disabled} aria-label={`选择 ${tileLabel(kind)}`} aria-pressed={selected}>{content}</button> : <span className={cls} aria-label={tileLabel(kind)}>{content}</span>;
}
export function FormField({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) { return <label className="field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label>; }
export function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) { return <div className="modal-backdrop" onClick={e => { if (e.target === e.currentTarget) onClose(); }}><section className="modal" role="dialog" aria-modal="true" aria-label={title}><div className="modal-title"><h2>{title}</h2><button className="icon-button" aria-label="关闭" onClick={onClose}>×</button></div>{children}</section></div>; }
