import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { AutoUpdate, bytes, fmtDate, get, Metric, post, put, Site, TopEntry, TrafficHour, UptimeState, VulnReport } from '../api';
import { fmtBytes, fmtMs, fmtNum, fmtPct, LineChart, SERIES, STATUS } from '../charts';
import { Badge, ErrorBox, Field, Icon, Loading, Seg, Toggle, useLoad, useToast } from '../ui';

type Range = '24h' | '7d' | '30d';
const ranges: { v: Range; label: string }[] = [{ v: '24h', label: '24 hours' }, { v: '7d', label: '7 days' }, { v: '30d', label: '30 days' }];
const rangeSecs: Record<Range, number> = { '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400 };

type View = 'traffic' | 'performance' | 'resources' | 'uptime';
const views: { v: View; label: string }[] = [
  { v: 'traffic', label: 'Traffic' }, { v: 'performance', label: 'Performance' }, { v: 'resources', label: 'Resources' }, { v: 'uptime', label: 'Uptime' },
];

function Tile({ k, v, d, tone }: { k: string; v: string; d?: string; tone?: 'good' | 'warn' | 'bad' }) {
  return (
    <div className={'card stat tile' + (tone ? ' ' + tone : '')}>
      <div className="k">{k}</div>
      <div className="v">{v}</div>
      {d && <div className="d dim">{d}</div>}
    </div>
  );
}

// hourly fills the hours with no traffic so charts show gaps as zero.
function hourly(hours: TrafficHour[], range: Range) {
  const now = Math.floor(Date.now() / 1000 / 3600) * 3600;
  const start = now - rangeSecs[range] + 3600;
  const by = new Map(hours.map((h) => [h.hour, h]));
  const t: number[] = [];
  const rows: TrafficHour[] = [];
  for (let h = start; h <= now; h += 3600) {
    t.push(h);
    rows.push(by.get(h) ?? { hour: h, requests: 0, bytes: 0, s2: 0, s3: 0, s4: 0, s5: 0, bots: 0, ms: 0 });
  }
  return { t, rows };
}

const sum = (rows: TrafficHour[], f: (h: TrafficHour) => number) => rows.reduce((a, h) => a + f(h), 0);

function TopTable({ title, rows, unit = 'Requests', fmt, value = (e) => e.requests, empty = 'Nothing yet.', mono = true }: {
  title: string; rows: TopEntry[] | null | undefined; unit?: string; fmt?: (e: TopEntry) => string; value?: (e: TopEntry) => number; empty?: string; mono?: boolean;
}) {
  const list = rows ?? [];
  const max = Math.max(1e-9, ...list.map(value));
  return (
    <div className="card">
      <div className="ch"><h3>{title}</h3><span className="dim" style={{ fontSize: 12 }}>{unit}</span></div>
      {list.length === 0 ? <div className="empty">{empty}</div> : (
        <table className="toplist"><tbody>{list.map((r) => (
          <tr key={r.key}>
            <td className={mono ? 'mono' : ''} title={r.key}>
              <div className="topkey">{r.key}</div>
              <div className="bar thin"><i style={{ width: (value(r) / max) * 100 + '%', background: SERIES[0] }} /></div>
            </td>
            <td style={{ textAlign: 'right' }}><b>{fmt ? fmt(r) : fmtNum(r.requests)}</b></td>
          </tr>
        ))}</tbody></table>
      )}
    </div>
  );
}

export function AnalyticsTab({ s }: { s: Site }) {
  const [sp, setSp] = useSearchParams();
  const view = (sp.get('view') as View) || 'traffic';
  const [range, setRange] = useState<Range>('24h');
  const setView = (v: View) => { const n = new URLSearchParams(sp); n.set('view', v); setSp(n); };
  return (
    <div className="stack">
      <div className="toolbar">
        <Seg value={view} options={views} onChange={setView} />
        <div style={{ marginLeft: 'auto' }}><Seg value={range} options={ranges} onChange={setRange} /></div>
      </div>
      {view === 'traffic' && <TrafficView s={s} range={range} />}
      {view === 'performance' && <PerformanceView s={s} range={range} />}
      {view === 'resources' && <ResourcesView s={s} range={range} />}
      {view === 'uptime' && <UptimeView s={s} range={range} />}
    </div>
  );
}

function TrafficView({ s, range }: { s: Site; range: Range }) {
  const d = useLoad(() => get<{ hours: TrafficHour[]; top: Record<string, TopEntry[]>; visitors: Record<string, number> }>(`/sites/${s.id}/traffic?range=${range}`), [s.id, range], 60000);
  if (!d.data) return d.error ? <ErrorBox error={d.error} /> : <Loading />;
  const { t, rows } = hourly(d.data.hours, range);
  const req = sum(rows, (h) => h.requests);
  const errs = sum(rows, (h) => h.s4 + h.s5);
  const s5 = sum(rows, (h) => h.s5);
  const visitors = Object.values(d.data.visitors).reduce((a, b) => a + b, 0);
  const top = d.data.top;
  return (
    <>
      <div className="tiles">
        <Tile k="Requests" v={fmtNum(req)} d={req ? fmtNum(sum(rows, (h) => h.bots)) + ' from bots and tools' : 'No visits logged yet'} />
        <Tile k="Visitors" v={fmtNum(visitors)} d="Different IP addresses each day" />
        <Tile k="Bandwidth" v={fmtBytes(sum(rows, (h) => h.bytes))} d="Sent by nginx, before Cloudflare" />
        <Tile k="Server errors" v={req ? fmtPct((s5 / req) * 100) : '–'} d={`${fmtNum(errs)} 4xx and 5xx responses`} tone={req && s5 / req > 0.01 ? 'bad' : undefined} />
      </div>
      <div className="grid2">
        <div className="card"><div className="ch"><h3>Requests per hour</h3></div><div className="cb">
          <LineChart t={t} series={[{ name: 'Requests', values: rows.map((h) => h.requests) }]} step={3600} area />
        </div></div>
        <div className="card"><div className="ch"><h3>Bandwidth per hour</h3></div><div className="cb">
          <LineChart t={t} series={[{ name: 'Bandwidth', values: rows.map((h) => h.bytes), color: SERIES[2] }]} fmt={fmtBytes} step={3600} area />
        </div></div>
      </div>
      <div className="grid2">
        <TopTable title="Top pages" rows={top.page} />
        <TopTable title="Top referrers" rows={top.ref} empty="No visitors came from other sites." />
        <TopTable title="Top visitors (IP)" rows={top.ip} />
        <TopTable title="Browsers" rows={top.browser} mono={false} />
        <TopTable title="Pages using the most bandwidth" rows={top.bandwidth} unit="Bandwidth" fmt={(e) => fmtBytes(e.bytes)} value={(e) => e.bytes} />
        <TopTable title="Not found (404)" rows={top['404']} empty="No missing pages." />
      </div>
      {(top['5xx']?.length ?? 0) > 0 && <TopTable title="Server errors (5xx)" rows={top['5xx']} />}
      <p className="dim" style={{ fontSize: 12 }}>Counted from this site's nginx access log every five minutes. Requests Cloudflare answers from its cache never reach Berth, so they are not included.</p>
    </>
  );
}

interface PHPSlow { time: string; script: string; culprit: string; frames: string[] | null }
interface DBSlow { query: string; count: number; total_s: number; max_s: number; rows_examined: number; last: number }
interface Perf {
  slow_requests: { time: number; method: string; path: string; status: number; ms: number }[];
  slowest_pages: TopEntry[]; hours: TrafficHour[];
  logs: { php_slow: PHPSlow[]; culprits: { name: string; count: number }[]; db_slow: DBSlow[]; db_slow_on: boolean; php_slow_log: string };
}

function PerformanceView({ s, range }: { s: Site; range: Range }) {
  const d = useLoad(() => get<Perf>(`/sites/${s.id}/performance?range=${range}`), [s.id, range], 60000);
  const [open, setOpen] = useState<number | null>(null);
  if (!d.data) return d.error ? <ErrorBox error={d.error} /> : <Loading />;
  const p = d.data;
  const { t, rows } = hourly(p.hours, range);
  const req = sum(rows, (h) => h.requests);
  const avg = req ? sum(rows, (h) => h.ms) / req : 0;
  return (
    <>
      <div className="tiles">
        <Tile k="Average response" v={req ? fmtMs(avg) : '–'} d="Time nginx waited for each request" tone={avg > 1000 ? 'bad' : avg > 500 ? 'warn' : undefined} />
        <Tile k="Slow requests" v={fmtNum(p.slow_requests.length)} d="Took a second or more" tone={p.slow_requests.length ? 'warn' : undefined} />
        <Tile k="PHP slow traces" v={fmtNum(p.logs.php_slow.length)} d="Requests stuck for 5 s or more" />
        {s.db && <Tile k="Slow database queries" v={fmtNum(p.logs.db_slow.reduce((a, q) => a + q.count, 0))} d="One second or more" />}
      </div>
      <div className="card"><div className="ch"><h3>Average response time per hour</h3></div><div className="cb">
        <LineChart t={t} series={[{ name: 'Average response', values: rows.map((h) => (h.requests ? h.ms / h.requests : null)), color: SERIES[1] }]} fmt={fmtMs} step={3600} />
      </div></div>
      <div className="grid2">
        <TopTable title="Slowest pages on average" rows={p.slowest_pages} unit="Average" fmt={(e) => fmtMs(e.ms / Math.max(1, e.requests))} value={(e) => e.ms / Math.max(1, e.requests)} empty="No page requests yet." />
        <div className="card"><div className="ch"><h3>Slowest requests</h3></div>
          {p.slow_requests.length === 0 ? <div className="empty">No request took a second or more.</div> : (
            <table className="toplist"><tbody>{p.slow_requests.slice(0, 15).map((r, i) => (
              <tr key={i}><td className="mono"><div className="topkey" title={r.path}>{r.method} {r.path}</div><div className="dim" style={{ fontSize: 11 }}>{fmtDate(new Date(r.time * 1000).toISOString())} · HTTP {r.status}</div></td>
                <td style={{ textAlign: 'right' }}><b>{fmtMs(r.ms)}</b></td></tr>
            ))}</tbody></table>
          )}
        </div>
      </div>
      <div className="card"><div className="ch"><h3>Where PHP spends its time</h3><span className="dim" style={{ fontSize: 12 }}>From PHP's slow log</span></div>
        {p.logs.php_slow.length === 0 ? <div className="empty">No PHP request has run for 5 seconds yet. When one does, PHP records what it was doing, and the plugin or theme responsible shows here.</div> : <>
          {p.logs.culprits.length > 0 && <div className="cb" style={{ display: 'flex', gap: 8, flexWrap: 'wrap', borderBottom: '1px solid var(--line)' }}>
            {p.logs.culprits.map((c) => <Badge key={c.name} kind="w">{c.name} · {c.count}×</Badge>)}
          </div>}
          <table><tbody>{p.logs.php_slow.slice(0, 20).map((e, i) => (
            <tr key={i} className="click" onClick={() => setOpen(open === i ? null : i)}>
              <td style={{ whiteSpace: 'normal' }}>
                <div><b>{e.culprit || 'WordPress core or the app'}</b> <span className="dim mono" style={{ fontSize: 11 }}>{e.script}</span></div>
                {open === i ? <pre className="mono trace">{(e.frames ?? []).join('\n')}</pre> : <div className="dim mono" style={{ fontSize: 11 }}>{(e.frames ?? [])[0]}</div>}
              </td>
              <td className="muted" style={{ textAlign: 'right', verticalAlign: 'top' }}>{e.time}</td>
            </tr>
          ))}</tbody></table>
        </>}
      </div>
      {s.db && <div className="card"><div className="ch"><h3>Slow database queries</h3><span className="dim" style={{ fontSize: 12 }}>Grouped by query shape</span></div>
        {!p.logs.db_slow_on ? <div className="empty">MariaDB's slow query log is not on yet. Berth turns it on within the hour.</div> :
          p.logs.db_slow.length === 0 ? <div className="empty">No query from this site took a second or more.</div> : (
            <table><thead><tr><th>Query</th><th>Runs</th><th>Slowest</th><th className="hide-sm">Rows read</th></tr></thead><tbody>{p.logs.db_slow.slice(0, 20).map((q, i) => (
              <tr key={i}><td style={{ whiteSpace: 'normal', maxWidth: 520 }}><div className="mono sqlq">{q.query}</div></td><td>{q.count}</td><td>{fmtMs(q.max_s * 1000)}</td><td className="hide-sm">{fmtNum(q.rows_examined)}</td></tr>
            ))}</tbody></table>
          )}
      </div>}
    </>
  );
}

function ResourcesView({ s, range }: { s: Site; range: Range }) {
  const d = useLoad(() => get<{ metrics: Metric[]; workers: number; memory_mb: number }>(`/sites/${s.id}/resources?range=${range}`), [s.id, range], 60000);
  if (!d.data) return d.error ? <ErrorBox error={d.error} /> : <Loading />;
  const m = d.data.metrics;
  const t = m.map((x) => x.time);
  const last = m[m.length - 1];
  const step = m.length > 1 ? m[1].time - m[0].time : 300;
  const peak = Math.max(0, ...m.map((x) => x.cpu));
  return (
    <>
      <div className="tiles">
        <Tile k="CPU now" v={last ? fmtPct(last.cpu) : '–'} d={`Peak ${fmtPct(peak)} of one core`} />
        <Tile k="PHP memory now" v={last ? fmtBytes(last.mem) : '–'} d={`${d.data.workers} workers, ${d.data.memory_mb} MB limit each`} />
        <Tile k="Files" v={last ? bytes(last.disk) : '–'} />
        {s.db && <Tile k="Database" v={last ? bytes(last.db) : '–'} />}
      </div>
      <div className="grid2">
        <div className="card"><div className="ch"><h3>CPU</h3><span className="dim" style={{ fontSize: 12 }}>% of one core</span></div><div className="cb">
          <LineChart t={t} series={[{ name: 'CPU', values: m.map((x) => x.cpu) }]} fmt={fmtPct} step={step} area />
        </div></div>
        <div className="card"><div className="ch"><h3>Memory</h3><span className="dim" style={{ fontSize: 12 }}>PHP and other processes of this site</span></div><div className="cb">
          <LineChart t={t} series={[{ name: 'Memory', values: m.map((x) => x.mem), color: SERIES[5] }]} fmt={fmtBytes} step={step} area />
        </div></div>
      </div>
      <div className="card"><div className="ch"><h3>Disk use</h3></div><div className="cb">
        <LineChart t={t} series={[{ name: 'Files', values: m.map((x) => x.disk) }, ...(s.db ? [{ name: 'Database', values: m.map((x) => x.db) }] : [])]} fmt={fmtBytes} step={step} />
      </div></div>
      <p className="dim" style={{ fontSize: 12 }}>Measured every 15 seconds and kept as five-minute averages for 30 days. CPU and memory count every process running as {s.name}'s system user: PHP workers, WP-Cron and WP-CLI.</p>
    </>
  );
}

interface UptimeResp {
  state?: UptimeState; summary: { checks: number; up: number; avg_ms: number };
  buckets: { time: number; checks: number; up: number; avg_ms: number }[];
  incidents: { start: number; end: number; error: string }[];
  recent: { time: number; ok: boolean; status: number; ms: number; error?: string }[];
  monitored: boolean; global_off: boolean; url: string;
}

export function upColor(b: { checks: number; up: number }) {
  if (!b.checks) return 'var(--line)';
  if (b.up === b.checks) return STATUS.good;
  return b.up / b.checks >= 0.5 ? STATUS.warn : STATUS.bad;
}

function UptimeView({ s, range }: { s: Site; range: Range }) {
  const d = useLoad(() => get<UptimeResp>(`/sites/${s.id}/uptime?range=${range}`), [s.id, range], 30000);
  const [tip, setTip] = useState<string | null>(null);
  const toast = useToast();
  if (!d.data) return d.error ? <ErrorBox error={d.error} /> : <Loading />;
  const u = d.data;
  const pct = u.summary.checks ? (u.summary.up / u.summary.checks) * 100 : null;
  const st = u.state;
  const setMonitor = (on: boolean) => put('/sites/' + s.id, { ...s, no_monitor: !on }).then(() => { toast(on ? 'Uptime checks on' : 'Uptime checks off'); d.reload(); }).catch((e) => toast(e.message, true));
  const step = u.buckets.length > 1 ? u.buckets[1].time - u.buckets[0].time : 900;
  const fmtT = (x: number) => new Date(x * 1000).toLocaleString(undefined, { weekday: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  return (
    <>
      {u.global_off ? <div className="note">Uptime checks are off for every site (Settings → Monitoring).</div> : !u.monitored ? null : st && (
        <div className={'banner ' + (st.down ? 'bad' : 'good')}>
          <Icon name={st.down ? 'x' : 'check'} size={18} />
          <div><b>{st.down ? 'Down' : 'Up'}</b> since {fmtDate(st.since)}{st.down && st.last_error ? ': ' + st.last_error : ''}
            <div className="dim" style={{ fontSize: 12 }}>Last checked {fmtDate(st.last_check)} · {fmtMs(st.last_ms)}</div></div>
        </div>
      )}
      <div className="tiles">
        <Tile k="Uptime" v={pct == null ? '–' : (pct >= 99.995 ? '100' : pct.toFixed(2)) + '%'} d={`${fmtNum(u.summary.checks)} checks`} tone={pct != null && pct < 99 ? 'bad' : pct != null && pct < 99.9 ? 'warn' : pct != null ? 'good' : undefined} />
        <Tile k="Average response" v={u.summary.avg_ms ? fmtMs(u.summary.avg_ms) : '–'} d="From this server, through DNS and the proxy" />
        <Tile k="Outages" v={String(u.incidents.length)} d={u.incidents[0] ? 'Last ' + fmtT(u.incidents[0].start) : 'None in this period'} tone={u.incidents.length ? 'warn' : undefined} />
      </div>
      <div className="card"><div className="ch"><h3>Availability</h3>
        <Toggle on={u.monitored && !u.global_off} onChange={setMonitor} label="Check every minute" /></div><div className="cb">
        <div className="upbars" onPointerLeave={() => setTip(null)}>
          {u.buckets.map((b) => <i key={b.time} style={{ background: upColor(b) }}
            onPointerEnter={() => setTip(`${fmtT(b.time)} – ${fmtT(b.time + step)}: ${b.checks ? `${b.up} of ${b.checks} checks up` : 'no checks'}`)} />)}
        </div>
        <div className="row" style={{ justifyContent: 'space-between', fontSize: 11.5, marginTop: 6 }} >
          <span className="dim">{tip ?? fmtT(u.buckets[0]?.time ?? 0)}</span>{!tip && <span className="dim">now</span>}
        </div>
        <div className="lc-legend" style={{ marginTop: 10 }}>
          <span><i style={{ background: STATUS.good }} />Up</span><span><i style={{ background: STATUS.warn }} />Some checks failed</span>
          <span><i style={{ background: STATUS.bad }} />Down</span><span><i style={{ background: 'var(--line)' }} />No checks</span>
        </div>
        <div className="dim" style={{ fontSize: 12, marginTop: 8 }}>Berth requests <span className="mono">{u.url}</span> every minute, the way a visitor would. Two failures in a row count as down and are logged in Activity.</div>
      </div></div>
      <div className="card"><div className="ch"><h3>Response time</h3></div><div className="cb">
        <LineChart t={u.buckets.map((b) => b.time)} series={[{ name: 'Response time', values: u.buckets.map((b) => (b.up ? b.avg_ms : null)), color: SERIES[1] }]} fmt={fmtMs} step={step} />
      </div></div>
      <div className="card"><div className="ch"><h3>Outages</h3></div>
        {u.incidents.length === 0 ? <div className="empty">No outages in this period.</div> : (
          <table><thead><tr><th>Started</th><th>Lasted</th><th>Reason</th></tr></thead><tbody>{u.incidents.map((i) => (
            <tr key={i.start}><td>{fmtT(i.start)}</td><td>{i.end ? durText(i.end - i.start) : <Badge kind="b">Ongoing</Badge>}</td><td style={{ whiteSpace: 'normal' }}>{i.error}</td></tr>
          ))}</tbody></table>
        )}
      </div>
    </>
  );
}

function durText(sec: number) {
  if (sec < 60) return 'under a minute';
  if (sec < 3600) return Math.round(sec / 60) + ' min';
  return (sec / 3600).toFixed(1) + ' h';
}

// WordPress tab cards -----------------------------------------------------------

const sevKind: Record<string, string> = { critical: 'b', high: 'b', medium: 'w', low: 'n' };

export function VulnCard({ s }: { s: Site }) {
  const d = useLoad(() => get<VulnReport>(`/sites/${s.id}/vulns`), [s.id]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const scan = () => { setBusy(true); setErr(null); post<VulnReport>(`/sites/${s.id}/vulns/scan`).then(d.setData).catch(setErr).finally(() => setBusy(false)); };
  const r = d.data;
  const never = !r || !r.checked || new Date(r.checked).getFullYear() < 2000;
  return (
    <div className="card"><div className="ch"><h3>Security</h3>
      <button className="btn s" disabled={busy} onClick={scan}><Icon name="lock" size={13} />{busy ? 'Checking…' : 'Check now'}</button></div>
      <ErrorBox error={err} />
      {!r ? <Loading /> : never ? <div className="empty">Not checked yet. Berth checks WordPress, plugins and themes against the WPVulnerability database once a day.</div> : <>
        <div className={'cb banner-in ' + (r.vulns.length ? 'bad' : 'good')}>
          <Icon name={r.vulns.length ? 'x' : 'check'} size={16} />
          <div><b>{r.vulns.length ? `${r.vulns.length} known vulnerabilit${r.vulns.length === 1 ? 'y' : 'ies'}` : 'No known vulnerabilities'}</b>
            <div className="dim" style={{ fontSize: 12 }}>{r.items} items checked {fmtDate(r.checked)}{r.error ? ' · ' + r.error : ''}</div></div>
        </div>
        {r.vulns.length > 0 && <table><tbody>{r.vulns.map((v, i) => (
          <tr key={i}><td style={{ whiteSpace: 'normal' }}>
            <div><b>{v.name}</b> <span className="dim">{v.kind} {v.version}</span>{!v.active && v.kind !== 'core' && <span className="dim"> · inactive</span>}</div>
            <div style={{ fontSize: 12.5 }}>{v.title}</div>
            <div className="dim" style={{ fontSize: 12 }}>
              {v.unfixed ? 'No fix released yet. Consider removing it.' : v.fixed_in ? `Fixed in ${v.fixed_in}.` : 'Update to the latest version.'}
              {v.update && ` ${v.update} is available.`}
              {(v.links ?? []).slice(0, 2).map((l) => <a key={l} href={l} target="_blank" rel="noreferrer" style={{ marginLeft: 8 }}>{(v.ids ?? [])[0] || 'Details'}</a>)}
            </div>
          </td><td style={{ textAlign: 'right', verticalAlign: 'top' }}>{v.severity ? <Badge kind={sevKind[v.severity] ?? 'n'}>{v.severity}{v.score ? ' ' + v.score.toFixed(1) : ''}</Badge> : null}</td></tr>
        ))}</tbody></table>}
      </>}
    </div>
  );
}

const dayLabel: Record<string, string> = { daily: 'Every day', mon: 'Mondays', tue: 'Tuesdays', wed: 'Wednesdays', thu: 'Thursdays', fri: 'Fridays', sat: 'Saturdays', sun: 'Sundays' };

export function AutoUpdateCard({ s, onSaved }: { s: Site; onSaved: () => void }) {
  const init: AutoUpdate = s.auto_update?.day ? s.auto_update : { enabled: false, day: 'daily', time: '04:00', core: true, plugins: true, themes: true, rollback: true };
  const [a, setA] = useState<AutoUpdate>(init);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  const set = (p: Partial<AutoUpdate>) => setA({ ...a, ...p });
  const save = () => { setErr(null); put(`/sites/${s.id}/auto-update`, a).then(() => { toast('Update schedule saved'); onSaved(); }).catch(setErr); };
  return (
    <div className="card"><div className="ch"><h3>Automatic updates</h3>{s.auto_update?.enabled ? <Badge kind="g">On</Badge> : <Badge kind="n">Off</Badge>}</div><div className="cb">
      <ErrorBox error={err} />
      <Toggle on={a.enabled} onChange={(v) => set({ enabled: v })} label="Update on a schedule" help="Berth backs the site up, updates, then loads the home page and login page to check nothing broke." />
      {a.enabled && <>
        <div className="grid2">
          <Field label="When"><select className="input" value={a.day} onChange={(e) => set({ day: e.target.value })}>
            {Object.entries(dayLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
          <Field label="At"><input className="input" type="time" value={a.time} onChange={(e) => set({ time: e.target.value })} /></Field>
        </div>
        <div className="row" style={{ gap: 18, flexWrap: 'wrap', marginBottom: 6 }}>
          <Toggle on={a.core} onChange={(v) => set({ core: v })} label="Core" />
          <Toggle on={a.plugins} onChange={(v) => set({ plugins: v })} label="Plugins" />
          <Toggle on={a.themes} onChange={(v) => set({ themes: v })} label="Themes" />
        </div>
        <Toggle on={a.rollback} onChange={(v) => set({ rollback: v })} label="Roll back if the site breaks" help="Restores the backup taken just before, if the site worked before updating and fails the check after." />
      </>}
      <button className="btn p s" onClick={save}>Save schedule</button>
    </div></div>
  );
}

export function SearchReplaceCard({ s, startJob }: { s: Site; startJob: (p: Promise<{ job_id: number }>, m?: string) => void }) {
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [guids, setGuids] = useState(false);
  const [backup, setBackup] = useState(true);
  const [preview, setPreview] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const body = (dry: boolean) => ({ from, to, guids, backup, dry_run: dry });
  const dry = () => { setBusy(true); setErr(null); setPreview(null); post<{ output: string }>(`/sites/${s.id}/wp/search-replace`, body(true)).then((r) => setPreview(r.output)).catch(setErr).finally(() => setBusy(false)); };
  const run = () => confirm(`Replace every "${from}" with "${to}" in the database?`) && startJob(post(`/sites/${s.id}/wp/search-replace`, body(false)), 'Search and replace started');
  return (
    <div className="card"><div className="ch"><h3>Search and replace</h3></div><div className="cb">
      <p className="muted" style={{ fontSize: 13, marginBottom: 12 }}>Change text everywhere in the database, such as an old address after a move. WP-CLI does the work, so serialized plugin settings stay valid.</p>
      <ErrorBox error={err} />
      <div className="grid2">
        <Field label="Search for"><input className="input mono" value={from} onChange={(e) => { setFrom(e.target.value); setPreview(null); }} placeholder="http://old.example.com" /></Field>
        <Field label="Replace with"><input className="input mono" value={to} onChange={(e) => { setTo(e.target.value); setPreview(null); }} placeholder="https://example.com" /></Field>
      </div>
      <Toggle on={backup} onChange={setBackup} label="Back up first" />
      <Toggle on={guids} onChange={setGuids} label="Also change post GUIDs" help="Leave off unless you know you need it; feed readers use GUIDs to spot new posts." />
      <div className="row" style={{ gap: 8 }}>
        <button className="btn s" disabled={!from || busy} onClick={dry}><Icon name="search" size={13} />{busy ? 'Counting…' : 'Preview'}</button>
        <button className="btn s p" disabled={!from || preview === null} onClick={run} title={preview === null ? 'Preview first' : ''}>Replace</button>
      </div>
      {preview !== null && <pre className="mono trace" style={{ marginTop: 12 }}>{preview || 'No matches.'}</pre>}
    </div></div>
  );
}
