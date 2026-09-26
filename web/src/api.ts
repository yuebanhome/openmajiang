export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) { super(message); this.name = 'APIError'; this.status = status; this.code = code; }
}
const messages:Record<string,string>={AUTH_EXPIRED:'登录已过期，请重新登录。',ACCOUNT_NOT_ELIGIBLE:'请先验证邮箱，并确认账号可用。',ALREADY_SEATED:'你已在另一张牌桌入座，请先返回当前牌桌。',OWNER_ALREADY_SEATED_OR_QUEUED:'你已入座或正在排队，请先结束当前操作。',NOT_ROOM_OWNER:'只有房主可以进行这项操作。',ROOM_FULL:'房间已满，你仍可观看弃牌。',INVALID_INVITE:'邀请码无效或已更新，请向房主索取新邀请。',STALE_CONTROL:'当前控制权已变更。请接管控制后重新同步。',STALE_DECISION:'这个动作窗口已经结束，正在等待新的状态。',STALE_WINDOW:'这个响应窗口已经结束，请等待下一步。',BOT_OFFLINE:'Bot 尚未连接，请先启动你的 Bot 进程。',RATE_LIMITED:'请求太频繁，请稍后再试。',SELF_TEST_REQUIRES_INVITE:'Bot 自测桌需要选择邀请入座。',INVALID_REQUEST:'提交的信息不完整或格式不符合要求。',CSRF_FAILED:'会话校验已失效，请刷新页面后重试。'};
let csrfToken = '';
export function clearAuthState() { csrfToken = ''; }
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body) headers.set('Content-Type', 'application/json');
  if (csrfToken && init.method && !['GET', 'HEAD'].includes(init.method)) headers.set('X-CSRF-Token', csrfToken);
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin', cache: 'no-store' });
  const token = response.headers.get('X-CSRF-Token');
  if (token) csrfToken = token;
  const data = response.status === 204 ? {} : await response.json().catch(() => ({}));
  if (typeof data.csrf_token === 'string') csrfToken = data.csrf_token;
  if(response.status===401 && !path.startsWith('/v1/auth/') && path!=='/v1/me')window.dispatchEvent(new Event('openmajiang:auth-expired'));
  if (!response.ok) throw new APIError(response.status, data.error?.code ?? data.code ?? 'request_failed', messages[data.error?.code ?? data.code] ?? data.error?.message ?? (typeof data.error === 'string' ? data.error : data.message) ?? `请求未完成（${response.status}）`);
  return data as T;
}
export const post = <T = Record<string, unknown>>(path: string, data: unknown = {}) => api<T>(path, { method: 'POST', body: JSON.stringify(data) });
export function listFrom<T>(data: unknown, key: string): T[] { if (Array.isArray(data)) return data; if (data && typeof data === 'object' && Array.isArray((data as Record<string, unknown>)[key])) return (data as Record<string, unknown>)[key] as T[]; return []; }
export function errorMessage(error: unknown) { return error instanceof Error ? error.message : '连接暂时不可用，请稍后重试。'; }
export function commandID() { return crypto.randomUUID(); }
