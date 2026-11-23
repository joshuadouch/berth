import { Component, createContext, ErrorInfo, ReactNode, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ApiError, get, Job } from './api';

const P: Record<string, ReactNode> = {
  dash: <><rect x="3" y="3" width="7" height="9" rx="1" /><rect x="14" y="3" width="7" height="5" rx="1" /><rect x="14" y="12" width="7" height="9" rx="1" /><rect x="3" y="16" width="7" height="5" rx="1" /></>,
  host: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18" /></>,
  pool: <><rect x="3" y="4" width="18" height="6" rx="1.5" /><rect x="3" y="14" width="18" height="6" rx="1.5" /><path d="M7 7h.01M7 17h.01" /></>,
  vip: <><path d="M12 2v6M12 16v6M2 12h6M16 12h6" /><circle cx="12" cy="12" r="4" /></>,
  cert: <><rect x="4" y="10" width="16" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3" /></>,
  cloud: <path d="M7 18a5 5 0 1 1 1.1-9.9A6 6 0 0 1 19 11a3.5 3.5 0 0 1-1 7z" />,
  node: <><rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" /></>,
  hist: <><path d="M3 12a9 9 0 1 0 3-6.7L3 8" /><path d="M3 3v5h5M12 7v5l3 2" /></>,
  set: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1" /></>,
  pulse: <path d="M3 12h4l3-8 4 16 3-8h4" />,
  plus: <path d="M12 5v14M5 12h14" />,
  check: <path d="M20 6L9 17l-5-5" />,
  x: <path d="M18 6L6 18M6 6l12 12" />,
  sync: <><path d="M21 12a9 9 0 0 1-15.5 6.2L3 16M3 12a9 9 0 0 1 15.5-6.2L21 8" /><path d="M21 3v5h-5M3 21v-5h5" /></>,
  tunnel: <><path d="M3 20V12a9 9 0 0 1 18 0v8" /><path d="M8 20v-7a4 4 0 0 1 8 0v7" /></>,
  globe: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18" /></>,
  lan: <><rect x="9" y="2" width="6" height="5" rx="1" /><rect x="2" y="17" width="6" height="5" rx="1" /><rect x="16" y="17" width="6" height="5" rx="1" /><path d="M12 7v5M5 17v-3h14v3" /></>,
  trash: <><path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14" /></>,
  lock: <><path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z" /><path d="M9 12l2 2 4-4" /></>,
  menu: <path d="M4 6h16M4 12h16M4 18h16" />,
  out: <><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" /></>,
  site: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M3 9h18M7 6.5h.01M10 6.5h.01" /></>,
  wp: <><circle cx="12" cy="12" r="9" /><path d="M5.5 8.5l4 11 2.5-7 2.5 7 4-11M4 8.5h4M10.5 8.5h3.5M16 8.5h4" /></>,
  php: <><path d="M8 7l-5 5 5 5M16 7l5 5-5 5M13.5 5l-3 14" /></>,
  db: <><ellipse cx="12" cy="5.5" rx="8" ry="3" /><path d="M4 5.5v13c0 1.7 3.6 3 8 3s8-1.3 8-3v-13M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3" /></>,
  backup: <><path d="M4 7h16v13H4zM2 3h20v4H2zM10 12h4" /></>,
  server: <><rect x="3" y="3" width="18" height="7" rx="1.5" /><rect x="3" y="14" width="18" height="7" rx="1.5" /><path d="M7 6.5h.01M7 17.5h.01" /></>,
  ext: <><path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" /></>,
  down: <><path d="M12 4v12M6 11l6 6 6-6M4 20h16" /></>,
  key: <><circle cx="8" cy="15" r="4" /><path d="M11 12l9-9M17 6l3 3M15 8l2 2" /></>,
  copy: <><rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3" /></>,
  file: <><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z" /><path d="M14 3v5h5M9 13h6M9 17h6" /></>,
  folder: <path d="M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z" />,
  up: <><path d="M12 20V8M6 13l6-6 6 6M4 4h16" /></>,
  edit: <><path d="M4 20h4L19 9l-4-4L4 16z" /><path d="M13.5 6.5l4 4" /></>,
  link: <><path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1" /><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1" /></>,
  zip: <><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z" /><path d="M14 3v5h5M10 5h2M10 8h2M10 11h2M10 14h2v3h-2z" /></>,
  search: <><circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" /></>,
  play: <path d="M7 4l13 8-13 8z" />,
  mail: <><rect x="3" y="5" width="18" height="14" rx="2" /><path d="M3 7l9 6 9-6" /></>,
  plug: <><path d="M9 2v6M15 2v6M6 8h12v4a6 6 0 0 1-12 0zM12 18v4" /></>,
  back: <path d="M15 18l-6-6 6-6" />,
  user: <><circle cx="12" cy="8" r="4" /><path d="M4 21a8 8 0 0 1 16 0" /></>,
  users: <><circle cx="9" cy="8" r="3.5" /><path d="M2.5 20a6.5 6.5 0 0 1 13 0" /><path d="M16 4.5a3.5 3.5 0 0 1 0 7M18 14a6.5 6.5 0 0 1 3.5 6" /></>,
  layers: <><path d="M12 3l9 5-9 5-9-5 9-5z" /><path d="M3 13l9 5 9-5" /></>,
};

export function Icon({ name, size = 16 }: { name: string; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" style={{ flex: 'none' }}>{P[name]}</svg>
  );
}

// BrandMark is the panel's logo: an uploaded image, or Berth's icon and name.
export function BrandMark({ logo, name }: { logo?: string; name?: string }) {
  if (logo) return <img className="brandlogo" src={'/api/brand/logo?v=' + encodeURIComponent(logo)} alt={name || 'Berth'} />;
  return <><Logo />{name || 'Berth'}</>;
}

// useFavicon points the browser tab's icon at the uploaded favicon.
export function useFavicon(favicon?: string) {
  useEffect(() => {
    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (!link) return;
    if (!link.dataset.berth) link.dataset.berth = link.href;
    link.href = favicon ? '/api/brand/favicon?v=' + encodeURIComponent(favicon) : link.dataset.berth;
  }, [favicon]);
}

export function Logo() {
  return (
    <div className="logo">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#fff" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 15h18l-2.5 5h-13zM6.5 15V9h11v6M12 4v5" /></svg>
    </div>
  );
}

const avatarGradients = [
  'linear-gradient(135deg,#8f6bff,#5b3df5)', 'linear-gradient(135deg,#ff7ab6,#e5488f)', 'linear-gradient(135deg,#34d3c4,#0ea5a4)',
  'linear-gradient(135deg,#ffb15c,#f37a20)', 'linear-gradient(135deg,#6aa4ff,#2f6fec)', 'linear-gradient(135deg,#4cd68b,#0f9d58)',
];

// SiteAvatar is a coloured tile with a site's initials, the same colour every time.
export function SiteAvatar({ name, small }: { name: string; small?: boolean }) {
  const clean = name.replace(/^www\./, '');
  let h = 0;
  for (const c of clean) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const parts = clean.split('.')[0].split(/[-_]/);
  const initials = (parts.length > 1 ? parts[0][0] + parts[1][0] : clean.slice(0, 2)).toUpperCase();
  return <div className={'siteav' + (small ? ' s' : '')} style={{ background: avatarGradients[h % avatarGradients.length] }}>{initials}</div>;
}

export function Badge({ kind = 'n', children }: { kind?: string; children: ReactNode }) {
  return <span className={'badge ' + kind}>{children}</span>;
}

export function Toggle({ on, onChange, label, help }: { on: boolean; onChange: (v: boolean) => void; label?: ReactNode; help?: ReactNode }) {
  const t = <button type="button" className={'toggle' + (on ? ' on' : '')} onClick={() => onChange(!on)} aria-pressed={on} />;
  if (!label) return t;
  return (
    <div className="row" style={{ gap: 10, alignItems: 'flex-start', marginBottom: 14 }}>
      {t}
      <div style={{ cursor: 'pointer' }} onClick={() => onChange(!on)}>
        <div>{label}</div>
        {help && <div className="dim" style={{ fontSize: 11.5 }}>{help}</div>}
      </div>
    </div>
  );
}

export function Seg<T extends string>({ value, options, onChange }: { value: T; options: { v: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div className="seg">
      {options.map((o) => (
        <span key={o.v} className={o.v === value ? 'on' : ''} onClick={() => onChange(o.v)}>{o.label}</span>
      ))}
    </div>
  );
}

export function Field({ label, children, help, style }: { label: string; children: ReactNode; help?: ReactNode; style?: React.CSSProperties }) {
  return (
    <div className="field" style={style}>
      <label>{label}</label>
      {children}
      {help && <small className="help">{help}</small>}
    </div>
  );
}

export function Chips({ values, onChange, placeholder, mono }: { values: string[]; onChange: (v: string[]) => void; placeholder?: string; mono?: boolean }) {
  const [draft, setDraft] = useState('');
  const commit = () => {
    const parts = draft.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
    if (parts.length) onChange([...values, ...parts.filter((p) => !values.includes(p))]);
    setDraft('');
  };
  return (
    <div className="input chips">
      {values.map((v) => (
        <span key={v} className={'chip' + (mono ? ' mono' : '')}>{v}
          <button type="button" onClick={() => onChange(values.filter((x) => x !== v))}><Icon name="x" size={11} /></button>
        </span>
      ))}
      <input value={draft} placeholder={placeholder} onChange={(e) => setDraft(e.target.value)} onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',' || e.key === ' ') { e.preventDefault(); commit(); }
          if (e.key === 'Backspace' && !draft && values.length) onChange(values.slice(0, -1));
        }} />
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const e = error as ApiError;
  return (
    <div className="err">
      {e.message || String(error)}
      {e.output && <div className="mono" style={{ marginTop: 8, opacity: .85 }}>{e.output}</div>}
    </div>
  );
}

export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [onClose]);
  // Rendered on <body> so page layout rules (such as the content width cap)
  // can never shrink the backdrop.
  return createPortal(
    <div className="modal-bg" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={'card modal' + (wide ? ' wide' : '')}>
        <div className="ch"><h3>{title}</h3><button className="btn s ghost" onClick={onClose}><Icon name="x" size={14} /></button></div>
        <div className="cb">{children}</div>
      </div>
    </div>,
    document.body,
  );
}

// Toasts -------------------------------------------------------------------
const ToastCtx = createContext<(msg: string, bad?: boolean) => void>(() => {});
export function ToastProvider({ children }: { children: ReactNode }) {
  const [t, setT] = useState<{ msg: string; bad: boolean } | null>(null);
  const timer = useRef<number>();
  const show = useCallback((msg: string, bad = false) => {
    setT({ msg, bad });
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setT(null), 4000);
  }, []);
  return (
    <ToastCtx.Provider value={show}>
      {children}
      {t && <div className={'toast' + (t.bad ? ' bad' : '')}>{t.msg}</div>}
    </ToastCtx.Provider>
  );
}
export const useToast = () => useContext(ToastCtx);

// Data loading -------------------------------------------------------------
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[] = [], pollMs = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [n, setN] = useState(0);
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    let alive = true;
    const run = () => {
      setLoading(true);
      return fn().then((d) => { if (alive) { setData(d); setError(null); } }).catch((e) => alive && setError(e)).finally(() => alive && setLoading(false));
    };
    run();
    const id = pollMs ? window.setInterval(run, pollMs) : 0;
    return () => { alive = false; if (id) window.clearInterval(id); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, n]);
  return { data, error, loading, reload: () => setN((x) => x + 1), setData };
}

export function Loading() {
  return <div className="empty">Loading…</div>;
}

export function StatusBadge({ status, enabled = true }: { status: string; enabled?: boolean }) {
  if (status === 'provisioning') return <Badge kind="a">Creating…</Badge>;
  if (status === 'deleting') return <Badge kind="w">Deleting…</Badge>;
  if (status === 'error') return <Badge kind="b">● Failed</Badge>;
  if (!enabled) return <Badge kind="w">Disabled</Badge>;
  return <Badge kind="g">● Live</Badge>;
}

export function TypeBadge({ type }: { type: string }) {
  if (type === 'wordpress') return <Badge kind="a"><Icon name="wp" size={11} /> WordPress</Badge>;
  if (type === 'php_db') return <Badge kind="v"><Icon name="db" size={11} /> PHP + database</Badge>;
  return <Badge kind="n"><Icon name="php" size={11} /> PHP</Badge>;
}

export function JobBadge({ status }: { status: string }) {
  if (status === 'running') return <Badge kind="a">Running</Badge>;
  if (status === 'ok') return <Badge kind="g">Done</Badge>;
  return <Badge kind="b">Failed</Badge>;
}

// Copyable shows a value with a copy button.
export function Copyable({ value, mono = true }: { value: string; mono?: boolean }) {
  const toast = useToast();
  return (
    <div className="secret">
      <code className={'input' + (mono ? ' mono' : '')}>{value}</code>
      <button type="button" className="btn s ghost" title="Copy" onClick={() => navigator.clipboard?.writeText(value).then(() => toast('Copied'), () => toast('Copy failed', true))}><Icon name="copy" size={13} /></button>
    </div>
  );
}

// JobView follows a background job until it ends.
export function JobView({ id, onDone }: { id: number; onDone?: (j: Job) => void }) {
  const [job, setJob] = useState<Job | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const done = useRef(false);
  useEffect(() => {
    let alive = true;
    let t = 0;
    const tick = () => get<Job>('/jobs/' + id).then((j) => {
      if (!alive) return;
      setJob(j);
      if (j.status === 'running') t = window.setTimeout(tick, 1000);
      else if (!done.current) { done.current = true; onDone?.(j); }
    }).catch(() => { if (alive) t = window.setTimeout(tick, 2000); });
    tick();
    return () => { alive = false; window.clearTimeout(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);
  useEffect(() => { if (box.current) box.current.scrollTop = box.current.scrollHeight; }, [job?.log]);
  if (!job) return <Loading />;
  return (
    <div>
      <div className="row" style={{ alignItems: 'center', gap: 10, marginBottom: 10 }}><b>{job.title}</b><JobBadge status={job.status} /></div>
      <div ref={box} className="code joblog mono">{job.log || 'Starting…'}</div>
    </div>
  );
}


// PageBoundary shows a message instead of a blank page when a page crashes.
// It resets when the address changes.
export class PageBoundary extends Component<{ children: ReactNode; resetKey: string }, { error: Error | null; key: string }> {
  state = { error: null as Error | null, key: this.props.resetKey };
  static getDerivedStateFromProps(p: { resetKey: string }, s: { error: Error | null; key: string }) {
    return p.resetKey !== s.key ? { error: null, key: p.resetKey } : null;
  }
  static getDerivedStateFromError(error: Error) { return { error }; }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error(error, info.componentStack); }
  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="card"><div className="cb">
        <div className="err"><b>This page hit an error.</b> {this.state.error.message}</div>
        <button className="btn s" style={{ marginTop: 12 }} onClick={() => this.setState({ error: null })}>Try again</button>
      </div></div>
    );
  }
}
