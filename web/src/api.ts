export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) { super(message); this.name = 'APIError'; this.status = status; this.code = code; }
}
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
  if (!response.ok) throw new APIError(response.status, data.error?.code ?? data.code ?? 'request_failed', data.error?.message ?? (typeof data.error === 'string' ? data.error : data.message) ?? `请求未完成（${response.status}）`);
  return data as T;
}
export const post = <T = Record<string, unknown>>(path: string, data: unknown = {}) => api<T>(path, { method: 'POST', body: JSON.stringify(data) });
export function listFrom<T>(data: unknown, key: string): T[] { if (Array.isArray(data)) return data; if (data && typeof data === 'object' && Array.isArray((data as Record<string, unknown>)[key])) return (data as Record<string, unknown>)[key] as T[]; return []; }
export function errorMessage(error: unknown) { return error instanceof Error ? error.message : '连接暂时不可用，请稍后重试。'; }
export function commandID() { return crypto.randomUUID(); }
