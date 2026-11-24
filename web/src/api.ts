export type SiteType = 'wordpress' | 'php_db' | 'php';

export interface Database { name: string; user: string; password?: string }
export interface WordPress { title: string; admin_user: string; admin_email: string; locale: string }
export interface SFTP { enabled: boolean; keys: string[] | null; password_set: boolean }
export interface HalyardLink { enabled: boolean; host_id: string; vip_id: string; exposure: string; cert_id: string; no_dns: boolean; error: string }
export interface Site {
  id: string; name: string; type: SiteType; domain: string; aliases: string[] | null; php_version: string; doc_root: string;
  redirect_aliases: boolean; front_controller: boolean; https: boolean; tls_mode: '' | 'local'; tls: SiteTLS; enabled: boolean; upload_mb: number; memory_mb: number;
  max_children: number; backups: boolean; db?: Database; wp?: WordPress; sftp: SFTP; halyard: HalyardLink; status: string;
  status_error: string; notes: string; created_at: string; nginx_custom?: string; account_id: string;
  php: PHPSettings; auto_update: AutoUpdate; no_monitor: boolean;
  import?: { files: string; files_name: string; db: string; db_name: string };
}
export interface SiteTLS { status: 'none' | 'pending' | 'active' | 'error' | ''; error?: string; domains?: string[]; issued?: string; expires?: string; fails?: number; next_try?: string }
export interface BrandSettings { name: string; support_email: string; support_url: string; ipv4: string; ipv6: string; logo?: string; favicon?: string }
export interface ACMESettings { email: string; staging: boolean; off: boolean }
// Who serves a site to visitors: Berth with Let's Encrypt, a proxy in front, or plain HTTP.
export type Serve = 'local' | 'proxy' | 'http';
export const serveOf = (s: { https: boolean; tls_mode: string }): Serve => (!s.https ? 'http' : s.tls_mode === 'local' ? 'local' : 'proxy');
export const serveFields = (v: Serve) => ({ https: v !== 'http', tls_mode: (v === 'local' ? 'local' : '') as '' | 'local' });
export interface PHPSettings { max_execution: number; max_input_vars: number; max_input_time: number; custom?: string }
export interface AutoUpdate { enabled: boolean; day: string; time: string; core: boolean; plugins: boolean; themes: boolean; rollback: boolean }
export interface UptimeState { down: boolean; since: string; fails: number; last_error?: string; last_check: string; last_ms: number }
export interface Vuln {
  kind: 'core' | 'plugin' | 'theme'; slug: string; name: string; version: string; title: string; severity: string; score: number;
  fixed_in: string; unfixed: boolean; links: string[] | null; ids: string[] | null; update: string; active: boolean;
}
export interface VulnReport { checked: string; vulns: Vuln[]; error?: string; items: number }
export interface TrafficHour { hour: number; requests: number; bytes: number; s2: number; s3: number; s4: number; s5: number; bots: number; ms: number }
export interface TopEntry { key: string; requests: number; bytes: number; ms: number }
export interface Metric { time: number; cpu: number; mem: number; disk: number; db: number }
export interface Usage { files: number; db: number }
export interface Backup { site: string; file: string; size: number; time: string; local: boolean; remote: boolean }
export interface FileEntry { name: string; dir: boolean; link?: string; size: number; mode: string; mtime: string }
export interface SiteRow extends Site { usage: Usage; last_backup: Backup | null; uptime: UptimeState | null; vulns: number }
export interface Job { id: number; site: string; kind: string; title: string; status: 'running' | 'ok' | 'failed'; log: string; started: string; finished: string; error: string }
export interface Event { id: number; time: string; level: string; kind: string; site: string; message: string }
export interface Service { name: string; label: string; active: string }
export interface Disk { path: string; total: number; free: number }
export interface User { id: string; username: string; email: string; totp_enabled: boolean; role: 'admin' | 'customer'; account_id: string }
export interface Account { id: string; name: string; email: string; notes: string; suspended: boolean; plan_id: string; external_id?: string; created_at: string }
export interface APIToken { id: string; name: string; scope: 'provisioning' | 'full'; prefix: string; created_by: string; created_at: string; last_used?: string }
export interface Plan {
  id: string; name: string; max_sites: number; disk_mb: number; dns_zones: number; max_domains: number; memory_mb: number; workers: number;
  upload_mb: number; db_connections: number; cpu_percent: number; ram_mb: number; max_processes: number; allow_shell: boolean; no_sftp: boolean;
  mail_domains: number; mailboxes: number; mail_aliases: number; mail_storage_mb: number;
}
export interface PlanUsage { plan: Plan | null; disk_used: number; sites: number }
export interface AccountView extends Account { users: User[]; sites: string[]; usage: PlanUsage }

export interface HalyardSettings { url: string; token?: string; pool_id: string; vip_id: string; exposure: string; insecure: boolean; auto_cert: boolean; no_dns: boolean }
export interface S3Settings { enabled: boolean; endpoint: string; region: string; bucket: string; prefix: string; access_key: string; secret_key?: string; path_style: boolean; keep: number; local_keep: number }
export interface HalyardVIP { id: string; address: string; label: string }
export interface HalyardPool { id: string; name: string; members: { name: string; address: string }[] | null }
export interface HalyardInfo { configured: boolean; error?: string; vips: HalyardVIP[]; pools: HalyardPool[]; pool_ok: boolean; local_addr: string; cloudflare?: { token_set: boolean; public_ip: string; tunnel_id: string } }
export const exposureLabel: Record<string, string> = { public: 'Public, proxied by Cloudflare', tunnel: 'Cloudflare Tunnel (proxied)', lan: 'LAN only (DNS to the VIP, not proxied)' };
export const vipLabel = (v: HalyardVIP) => (v.label ? `${v.label} (${v.address.split('/')[0]})` : v.address.split('/')[0]);
export interface Settings {
  trusted_proxies: string[] | null; proxy_only: boolean; default_php: string;
  backup_enabled: boolean; backup_time: string; backup_keep: number; backup_dir: string;
  wp_locale: string; wp_admin_email: string; wp_admin_user: string;
  access_team: string; access_aud: string; access_emails: string[] | null;
  halyard: HalyardSettings;
  s3: S3Settings;
  acme: ACMESettings;
  brand: BrandSettings;
  cloudflare_token?: string; no_uptime: boolean; no_vuln_scan: boolean;
}

export interface SiteDetail {
  site: Site; usage: Usage; backups: Backup[]; jobs: Job[]; events: Event[]; dir: string; user: string; socket: string; sftp_available: boolean;
  cloudflare: boolean; halyard: boolean; dns: boolean; uptime: UptimeState | null; vulns: number;
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

// putFile sends a file as the raw request body.
export async function putFile<T = any>(path: string, file: File): Promise<T> {
  const res = await fetch('/api' + path, { method: 'PUT', credentials: 'same-origin', headers: { 'X-Berth': '1' }, body: file });
  let data: any = null;
  try { data = await res.json(); } catch { /* empty */ }
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText, data);
  return data as T;
}

// upload sends files as multipart; names may hold folders ("dir/file").
export async function upload<T = void>(path: string, files: { file: File; name: string }[], onProgress?: (pct: number) => void): Promise<T> {
  const fd = new FormData();
  for (const f of files) fd.append('file', f.file, f.name);
  return new Promise<T>((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open('POST', '/api' + path);
    x.setRequestHeader('X-Berth', '1');
    x.upload.onprogress = (e) => e.lengthComputable && onProgress?.(Math.round((e.loaded / e.total) * 100));
    x.onload = () => {
      if (x.status < 300) { try { return resolve(JSON.parse(x.responseText)); } catch { return resolve(undefined as T); } }
      let msg = x.statusText;
      try { msg = JSON.parse(x.responseText).error || msg; } catch { /* not JSON */ }
      reject(new ApiError(x.status, msg, null));
    };
    x.onerror = () => reject(new ApiError(0, 'upload failed', null));
    x.send(fd);
  });
}
export interface UploadInfo { id: string; name: string; size: number; kind: string }
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

// Plugins ------------------------------------------------------------------
export interface PluginField {
  key: string; label: string; type: 'text' | 'secret' | 'bool' | 'number' | 'select' | 'textarea'; section?: string;
  help?: string; placeholder?: string; default?: unknown; options?: { value: string; label: string }[]; required?: boolean;
}
export interface PluginView {
  id: string; name: string; description: string; version: string; icon: string; nav_label: string; fields: PluginField[];
  enabled: boolean; config: Record<string, any>; secrets_set: Record<string, boolean>; can_test: boolean;
  per_account: boolean; admin_only: boolean; accounts: string[];
}
export interface PluginLink { id: string; name: string; icon: string; nav_label: string }
// Other parts of the UI (the sidebar) listen for this after plugins change.
export const pluginsChanged = () => window.dispatchEvent(new Event('berth-plugins'));
