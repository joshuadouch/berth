import { Link, useNavigate } from 'react-router-dom';
import { ago, bytes, Disk, Event, get, Job, Metric, PlanUsage, Service, Site, TrafficHour, UptimeState } from '../api';
import { fmtBytes, fmtNum, fmtPct, LineChart, SERIES } from '../charts';
import { useMe } from '../me';
import { Badge, Copyable, ErrorBox, Icon, JobBadge, Loading, StatusBadge, TypeBadge, useLoad } from '../ui';

interface Dash {
  sites: number; counts: Record<string, number>; attention: Site[]; services: Service[]; disks: Disk[];
  backups: number; backup_bytes: number; jobs: Job[]; events: Event[];
  settings: { backup_enabled: boolean; backup_time: string; trusted_proxies: string[] | null; halyard: boolean };
  traffic: TrafficHour[]; uptime: Record<string, { checks: number; up: number; avg_ms: number }>;
  uptime_state: Record<string, UptimeState>; vulns: Record<string, number>; resources: Metric[]; customer?: boolean; plan?: PlanUsage;
}

export function ServiceList({ services }: { services: Service[] }) {
  return (
    <table><tbody>{services.map((s) => (
      <tr key={s.name}><td>{s.label}</td><td className="mono dim">{s.name}</td>
        <td style={{ textAlign: 'right' }}>{s.active === 'active' ? <Badge kind="g">● Running</Badge> : s.active === 'unknown' ? <Badge kind="n">Unknown</Badge> : <Badge kind="b">● {s.active}</Badge>}</td></tr>
    ))}</tbody></table>
  );
}

export function DiskBars({ disks }: { disks: Disk[] }) {
  return <>{disks.map((d) => {
    const used = d.total - d.free;
    const pct = d.total ? Math.round((used / d.total) * 100) : 0;
    return (
      <div key={d.path} style={{ marginBottom: 14 }}>
        <div className="meter-k"><span className="mono">{d.path}</span><b>{bytes(used)} of {bytes(d.total)}</b></div>
        <div className="bar"><i style={{ width: pct + '%', background: pct > 90 ? 'var(--bad)' : pct > 75 ? 'var(--warn)' : 'var(--accent2)' }} /></div>
      </div>
    );
  })}</>;
}

function greeting() {
  const h = new Date().getHours();
  return h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening';
}

export default function Dashboard({ me }: { me: string }) {
  const { data, error } = useLoad(() => get<Dash>('/dashboard'), [], 15000);
  const nav = useNavigate();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const down = data.services.filter((s) => s.active !== 'active' && s.active !== 'unknown');
  const reqs = data.traffic.reduce((a, h) => a + h.requests, 0);
  const checks = Object.values(data.uptime).reduce((a, u) => ({ c: a.c + u.checks, up: a.up + u.up }), { c: 0, up: 0 });
  const sitesDown = Object.entries(data.uptime_state).filter(([, st]) => st.down).map(([n]) => n);
  const vulnSites = Object.keys(data.vulns);
  const res = data.resources;
  const cust = !!data.customer;
  // Mail-only accounts have no websites: show them their email instead.
  const mailOnly = cust && !!data.plan?.plan?.no_sites;
  if (mailOnly) return (
    <>
      <div className="hero">
        <div className="head">
          <div><h1>{greeting()}, {me}</h1><div className="sub">Your business email</div></div>
          <Link to="/p/mailcow" className="btn white"><Icon name="mail" />Mail</Link>
        </div>
      </div>
      <MailStart />
      <div className="card">
        <div className="ch"><h3>Activity</h3></div>
        {data.events.length === 0 ? <div className="empty">No activity yet.</div> :
          <table><tbody>{data.events.map((e) => (
            <tr key={e.id}><td>{e.level === 'error' ? <span className="dot b" /> : e.level === 'warn' ? <span className="dot w" /> : <span className="dot g" />}</td><td style={{ whiteSpace: 'normal' }}>{e.message}</td><td className="muted" style={{ textAlign: 'right' }}>{ago(e.time)}</td></tr>
          ))}</tbody></table>}
      </div>
    </>
  );
  return (
    <>
      <div className="hero">
        <div className="head">
          <div><h1>{greeting()}, {me}</h1>
            <div className="sub">{data.sites} site{data.sites === 1 ? '' : 's'} {cust ? 'in your account' : 'on this server'} · {sitesDown.length ? `${sitesDown.length} down right now` : 'all sites answering'}{vulnSites.length ? ` · ${vulnSites.length} with known vulnerabilities` : ''}</div></div>
          <Link to="/sites/new" className="btn white"><Icon name="plus" />New site</Link>
        </div>
      </div>
      {cust && data.plan?.plan?.disk_mb ? data.plan.disk_used >= data.plan.plan.disk_mb * 1048576
        ? <div className="err">Your account has used all {bytes(data.plan.plan.disk_mb * 1048576)} of its disk space, so new sites and uploads are blocked. Free some space or <Link to="/account">see your plan</Link>.</div>
        : data.plan.disk_used >= data.plan.plan.disk_mb * 1048576 * 0.9 ? <div className="warnbox">Your account has used {bytes(data.plan.disk_used)} of {bytes(data.plan.plan.disk_mb * 1048576)} disk space.</div> : null : null}
      {down.length > 0 && <div className="err">Not running: {down.map((s) => s.label).join(', ')}. Sites that use them are down.</div>}
      {!cust && !data.settings.trusted_proxies?.length && <div className="warnbox">No trusted proxies are set, so sites see the load balancer's address instead of the visitor's and cannot tell HTTPS from HTTP. Add your Halyard nodes on the <Link to="/settings">Settings</Link> page.</div>}
      {cust && data.sites === 0 && <GettingStarted />}
      <div className="row" style={{ marginBottom: 16 }}>
        <Link to="/sites" className="card stat mon-tile"><div className="k"><span className="ic violet"><Icon name="site" size={15} /></span>Sites</div><div className="v">{data.sites}</div><div className="d muted">{data.counts.wordpress ?? 0} WordPress · {(data.counts.php_db ?? 0) + (data.counts.php ?? 0)} PHP</div></Link>
        <Link to="/backups" className="card stat mon-tile"><div className="k"><span className="ic teal"><Icon name="backup" size={15} /></span>Backups</div><div className="v">{data.backups}</div><div className="d muted">{bytes(data.backup_bytes)}{cust ? '' : data.settings.backup_enabled ? ` · nightly at ${data.settings.backup_time}` : ' · nightly off'}</div></Link>
        {!cust && <Link to="/system" className="card stat mon-tile"><div className="k"><span className="ic blue"><Icon name="server" size={15} /></span>Services</div><div className="v">{data.services.length - down.length}/{data.services.length}</div><div className="d muted">running</div></Link>}
        <div className="card stat mon-tile"><div className="k"><span className="ic orange"><Icon name="pulse" size={15} /></span>Requests</div><div className="v">{fmtNum(reqs)}</div><div className="d muted">last 24 hours, all sites</div></div>
        <div className="card stat mon-tile"><div className="k"><span className="ic pink"><Icon name="check" size={15} /></span>Uptime</div><div className="v">{checks.c ? ((checks.up / checks.c) * 100).toFixed(2) + '%' : '–'}</div><div className="d muted">{sitesDown.length ? `${sitesDown.length} down now` : 'last 24 hours'}</div></div>
      </div>
      {(sitesDown.length > 0 || vulnSites.length > 0) && <div className="card" style={{ marginBottom: 16 }}>
        <div className="ch"><h3>Monitoring alerts</h3></div>
        <table><tbody>
          {sitesDown.map((n) => <tr key={'d' + n} className="click" onClick={() => nav(`/sites/${n}?tab=analytics&view=uptime`)}><td><b>{n}</b><div className="dim" style={{ fontSize: 12 }}>{data.uptime_state[n].last_error}</div></td><td style={{ textAlign: 'right' }}><Badge kind="b">● Down</Badge></td></tr>)}
          {vulnSites.map((n) => <tr key={'v' + n} className="click" onClick={() => nav(`/sites/${n}?tab=wordpress`)}><td><b>{n}</b><div className="dim" style={{ fontSize: 12 }}>Known vulnerabilities in WordPress, plugins or themes</div></td><td style={{ textAlign: 'right' }}><Badge kind="b"><Icon name="lock" size={11} />{data.vulns[n]}</Badge></td></tr>)}
        </tbody></table>
      </div>}
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <div className="stack" style={{ flex: 3, minWidth: 0 }}>
          {data.attention.length > 0 && <div className="card">
            <div className="ch"><h3>Needs attention</h3></div>
            <table><tbody>{data.attention.map((s) => (
              <tr key={s.id} className="click" onClick={() => nav('/sites/' + s.id)}><td><b>{s.domain}</b><div className="dim" style={{ fontSize: 12 }}>{s.status_error || 'Disabled'}</div></td><td><TypeBadge type={s.type} /></td><td style={{ textAlign: 'right' }}><StatusBadge status={s.status} enabled={s.enabled} /></td></tr>
            ))}</tbody></table>
          </div>}
          <div className="card">
            <div className="ch"><h3>Recent jobs</h3><Link to="/activity" className="btn s ghost">All activity</Link></div>
            {data.jobs.length === 0 ? <div className="empty">Nothing yet. Create your first site.</div> :
              <table><tbody>{data.jobs.map((j) => (
                <tr key={j.id} className="click" onClick={() => nav('/activity?job=' + j.id)}><td style={{ whiteSpace: 'normal', wordBreak: 'break-word' }}>{j.title}{j.error && <div className="dim" style={{ fontSize: 12, maxHeight: 36, overflow: 'hidden' }}>{j.error.split('\n')[0]}</div>}</td><td className="muted" style={{ whiteSpace: 'nowrap' }}>{ago(j.started)}</td><td style={{ textAlign: 'right' }}><JobBadge status={j.status} /></td></tr>
              ))}</tbody></table>}
          </div>
          <div className="card">
            <div className="ch"><h3>Activity</h3></div>
            {data.events.length === 0 ? <div className="empty">No activity yet.</div> :
              <table><tbody>{data.events.map((e) => (
                <tr key={e.id}><td>{e.level === 'error' ? <span className="dot b" /> : e.level === 'warn' ? <span className="dot w" /> : <span className="dot g" />}</td><td style={{ whiteSpace: 'normal' }}>{e.message}</td><td className="muted" style={{ textAlign: 'right' }}>{ago(e.time)}</td></tr>
              ))}</tbody></table>}
          </div>
        </div>
        {!cust && <div className="stack" style={{ flex: 2, minWidth: 0 }}>
          <div className="card"><div className="ch"><h3>Server load</h3><span className="dim" style={{ fontSize: 12 }}>24 hours</span></div><div className="cb">
            <div className="mon-sub">CPU, % of all cores</div>
            <LineChart t={res.map((m) => m.time)} series={[{ name: 'CPU', values: res.map((m) => m.cpu) }]} fmt={fmtPct} step={300} height={120} area max={100} />
            <div className="mon-sub" style={{ marginTop: 12 }}>Memory in use</div>
            <LineChart t={res.map((m) => m.time)} series={[{ name: 'Memory', values: res.map((m) => m.mem), color: SERIES[5] }]} fmt={fmtBytes} step={300} height={120} area />
          </div></div>
          <div className="card"><div className="ch"><h3>Services</h3></div><ServiceList services={data.services} /></div>
          <div className="card"><div className="ch"><h3>Disk</h3></div><div className="cb"><DiskBars disks={data.disks} /></div></div>
        </div>}
      </div>
    </>
  );
}

// MailStart walks a mail-only customer through setting up their email.
function MailStart() {
  const { me } = useMe();
  const b = me?.brand;
  const dns = me?.plugins.some((p) => p.id === 'dns');
  return (
    <div className="card" style={{ marginBottom: 16 }}><div className="ch"><h3>Getting started</h3></div><div className="cb">
      <ol className="steps" style={{ margin: 0, paddingLeft: 20, lineHeight: 1.7 }}>
        <li>Open <Link to="/p/mailcow">Mail</Link> and check your domain is listed. It shows the DNS records your domain needs.</li>
        {dns && <li>Easiest: add the domain under <Link to="/p/dns">DNS</Link> and set its nameservers at your registrar; the mail records are made for you.</li>}
        <li>Add mailboxes for your team, then read mail in webmail or any mail app.</li>
      </ol>
      {(b?.support_email || b?.support_url) && <small className="help">Stuck? {b.support_email && <a href={'mailto:' + b.support_email}>{b.support_email}</a>}{b.support_email && b.support_url && ' · '}{b.support_url && <a href={b.support_url} target="_blank" rel="noreferrer">Support</a>}</small>}
    </div></div>
  );
}

// GettingStarted walks a new hosting customer through their first site.
function GettingStarted() {
  const { me } = useMe();
  const b = me?.brand;
  const dns = me?.plugins.some((p) => p.id === 'dns');
  return (
    <div className="card" style={{ marginBottom: 16 }}><div className="ch"><h3>Getting started</h3></div><div className="cb">
      <ol className="steps" style={{ margin: 0, paddingLeft: 20, lineHeight: 1.7 }}>
        <li><Link to="/sites/new">Create a site</Link>: WordPress in one click, or PHP with or without a database. You can bring an existing site from a backup.</li>
        <li>Point your domain at this server{b?.ipv4 || b?.ipv6 ? ':' : ' (ask us for the address).'}
          {(b?.ipv4 || b?.ipv6) && <div className="row" style={{ gap: 12, margin: '6px 0', flexWrap: 'wrap', alignItems: 'center' }}>
            {b?.ipv4 && <><span className="muted">A record</span><Copyable value={b.ipv4} /></>}
            {b?.ipv6 && <><span className="muted">AAAA record</span><Copyable value={b.ipv6} /></>}
          </div>}
          {dns && <div className="muted">Or add the domain under <Link to="/p/dns">DNS</Link> and use our nameservers; the records are made for you.</div>}
        </li>
        <li>HTTPS sets itself up: a free certificate is issued once the domain points here.</li>
      </ol>
      {(b?.support_email || b?.support_url) && <small className="help">Stuck? {b.support_email && <a href={'mailto:' + b.support_email}>{b.support_email}</a>}{b.support_email && b.support_url && ' · '}{b.support_url && <a href={b.support_url} target="_blank" rel="noreferrer">Support</a>}</small>}
    </div></div>
  );
}
