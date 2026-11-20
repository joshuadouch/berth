export type SiteType = 'wordpress' | 'php_db' | 'php';

export interface Database { name: string; user: string; password?: string }
export interface WordPress { title: string; admin_user: string; admin_email: string; locale: string }
export interface SFTP { enabled: boolean; keys: string[] | null; password_set: boolean }
export interface HalyardLink { enabled: boolean; host_id: string; vip_id: string; exposure: string; cert_id: string; error: string }
export interface Site {
  id: string; name: string; type: SiteType; domain: string; aliases: string[] | null; php_version: string; doc_root: string;
  redirect_aliases: boolean; front_controller: boolean; https: boolean; enabled: boolean; upload_mb: number; memory_mb: number;
  max_children: number; backups: boolean; db?: Database; wp?: WordPress; sftp: SFTP; halyard: HalyardLink; status: string;
  status_error: string; notes: string; created_at: string;
}
export interface Usage { files: number; db: number }
export interface Backup { site: string; file: string; size: number; time: string }
export interface SiteRow extends Site { usage: Usage; last_backup: Backup | null }
export interface Job { id: number; site: string; kind: string; title: string; status: 'running' | 'ok' | 'failed'; log: string; started: string; finished: string; error: string }
export interface Event { id: number; time: string; level: string; kind: string; site: string; message: string }
export interface Service { name: string; label: string; active: string }
export interface Disk { path: string; total: number; free: number }
export interface User { id: string; username: string; email: string; totp_enabled: boolean }

export interface HalyardSettings { url: string; token?: string; pool_id: string; vip_id: string; exposure: string; insecure: boolean; auto_cert: boolean }
export interface Settings {
  trusted_proxies: string[] | null; proxy_only: boolean; default_php: string;
  backup_enabled: boolean; backup_time: string; backup_keep: number; backup_dir: string;
  wp_locale: string; wp_admin_email: string; wp_admin_user: string;
  access_team: string; access_aud: string; access_emails: string[] | null;
  halyard: HalyardSettings;
}

export interface SiteDetail {
  site: Site; usage: Usage; backups: Backup[]; jobs: Job[]; events: Event[]; dir: string; user: string; socket: string; sftp_available: boolean;
}
export interface WPInfo {
  version: string; core_update: string; siteurl: string; error?: string; plugin_updates: number; theme_updates: number;
  plugins: { name: string; status: string; version: string; update: string; title: string }[] | null;
  themes: { name: string; status: string; version: string; update: string; title: string }[] | null;
}

export class ApiError extends Error {
  status: number; output?: string; data: any;
  constructor(status: number, message: string, data: any) {
    super(message); this.status = status; this.output = data?.output; this.data = data;
  }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(f: () => void) { onUnauthorized = f; }

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch('/api' + path, {
    method,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Berth': '1' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data: any = null;
  try { data = await res.json(); } catch { /* empty */ }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/login') && !path.startsWith('/setup')) onUnauthorized();
    throw new ApiError(res.status, data?.error || res.statusText, data);
  }
  return data as T;
}

export const get = <T = any>(p: string) => api<T>('GET', p);
export const post = <T = any>(p: string, b?: unknown) => api<T>('POST', p, b ?? {});
export const put = <T = any>(p: string, b: unknown) => api<T>('PUT', p, b);
export const del = <T = any>(p: string) => api<T>('DELETE', p);

export function ago(iso: string): string {
  const t = new Date(iso).getTime();
  if (!t || new Date(iso).getFullYear() < 2000) return '';
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

export function fmtDate(iso: string): string {
  const d = new Date(iso);
  if (!d.getTime() || d.getFullYear() < 2000) return '—';
  return d.toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
}

export function bytes(n: number): string {
  if (!n) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)} ${u[i]}`;
}

export const typeLabel: Record<SiteType, string> = { wordpress: 'WordPress', php_db: 'PHP + database', php: 'PHP' };

// nameFromDomain mirrors model.NameFromDomain.
export function nameFromDomain(d: string): string {
  let n = d.toLowerCase().replace(/^www\./, '').replace(/[^a-z0-9]/g, '').replace(/^[0-9]+/, '').slice(0, 24);
  if (n.length < 2) n = ('site' + n).slice(0, 24);
  return n;
}

export function siteURL(s: Site) { return (s.https ? 'https://' : 'http://') + s.domain; }
