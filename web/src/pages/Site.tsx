import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { AccountView, ago, Backup, bytes, del, fmtDate, get, Job, post, put, Serve, serveFields, serveOf, Site, SiteDetail, siteURL, typeLabel, WPInfo } from '../api';
import { useMe } from '../me';
import FilesTab from './Files';
import { SiteDNSCard } from './DNS';
import { CloudflareDNSCard } from './Cloudflare';
import DatabaseTab from './Database';
import { AnalyticsTab, AutoUpdateCard, SearchReplaceCard, VulnCard } from './Insights';
import { BackupBrowser, RestoreModal, Where } from './BackupTools';
import { Badge, Chips, Copyable, ErrorBox, Field, Icon, JobBadge, JobView, Loading, Modal, SiteAvatar, StatusBadge, Toggle, TypeBadge, useLoad, useToast } from '../ui';

export const tabs = ['overview', 'analytics', 'files', 'database', 'wordpress', 'backups', 'logs', 'settings', 'activity'] as const;
export type Tab = typeof tabs[number];
export const tabIcon: Record<Tab, string> = { overview: 'dash', analytics: 'pulse', files: 'folder', database: 'db', wordpress: 'wp', backups: 'backup', logs: 'file', settings: 'set', activity: 'hist' };
// visibleTabs are the site sections that apply to a site in its current state.
export function visibleTabs(s: Site): Tab[] {
  const ready = s.status === 'active' || s.status === 'error';
  return tabs.filter((t) => (t !== 'wordpress' || s.type === 'wordpress') && (t !== 'database' || (!!s.db && ready)) && (t !== 'files' || ready) && (t !== 'analytics' || ready));
}
export const tabLabel: Record<Tab, string> = { overview: 'Overview', analytics: 'Analytics', files: 'Files', database: 'Database', settings: 'Settings', wordpress: 'WordPress', backups: 'Backups', logs: 'Logs', activity: 'Activity' };

export default function SitePage() {
  const { id = '' } = useParams();
  const [sp, setSp] = useSearchParams();
  const tab = (sp.get('tab') as Tab) || 'overview';
  const d = useLoad(() => get<SiteDetail>('/sites/' + id), [id], 5000);
  const [job, setJob] = useState<number | null>(null);
  const [jobsDone, setJobsDone] = useState(0);
  const toast = useToast();
  if (!d.data) return d.error ? <ErrorBox error={d.error} /> : <Loading />;
  const s = d.data.site;
  const visible = visibleTabs(s);
  const purge = () => post<{ hosts: number }>('/p/cloudflare/sites/' + s.id + '/purge').then((r) => toast(`Cloudflare cache cleared for ${r.hosts} hostname${r.hosts === 1 ? '' : 's'}`)).catch((e) => toast(e.message, true));
  const startJob = (p: Promise<{ job_id: number }>, msg?: string) => p.then((r) => { setJob(r.job_id); if (msg) toast(msg); d.reload(); }).catch((e) => toast(e.message, true));
  return (
    <>
      <div className="hero">
        <div className="head">
          <div className="row" style={{ gap: 16, alignItems: 'center', minWidth: 0 }}>
            <SiteAvatar name={s.domain} />
            <div style={{ minWidth: 0 }}>
              <div className="sitehead"><h1>{s.domain}</h1><TypeBadge type={s.type} /><StatusBadge status={s.status} enabled={s.enabled} />
                {d.data.uptime?.down && <Badge kind="b">● Down</Badge>}
                {d.data.vulns > 0 && <span className="click" onClick={() => setSp({ tab: 'wordpress' })}><Badge kind="b"><Icon name="lock" size={11} />{d.data.vulns} vulnerabilit{d.data.vulns === 1 ? 'y' : 'ies'}</Badge></span>}</div>
              <div className="sub">{s.name}{s.aliases?.length ? ' · also ' + s.aliases.join(', ') : ''}</div>
            </div>
          </div>
          <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
            {s.status === 'error' && <button className="btn white" onClick={() => startJob(post('/sites/' + s.id + '/retry'), 'Retrying')}><Icon name="sync" />Retry</button>}
            {d.data.cloudflare && s.status === 'active' && <button className="btn light" onClick={purge} title="Clear Cloudflare's cache for this site's domains"><Icon name="cloud" />Clear cache</button>}
            <a className="btn light" href={siteURL(s)} target="_blank" rel="noreferrer"><Icon name="ext" />Visit site</a>
            {s.type === 'wordpress' && s.status === 'active' && <WPAdminButton site={s} />}
          </div>
        </div>
        <div className="herostats">
          <div>PHP<b>{s.php_version}</b></div>
          <div>Disk<b>{bytes(d.data.usage.files + d.data.usage.db)}</b></div>
          <div>Last backup<b>{d.data.backups[0] ? ago(d.data.backups[0].time) : 'none'}</b></div>
          <div>Uptime check<b>{!d.data.uptime ? 'not yet' : d.data.uptime.down ? 'down ' + ago(d.data.uptime.since).replace(' ago', '') : 'up ' + ago(d.data.uptime.since).replace(' ago', '')}</b></div>
          <div>HTTPS<b>{httpsLabel(s)}</b></div>
        </div>
      </div>
      {s.status === 'error' && <div className="err">Creating this site failed: {s.status_error}. Fix the cause (see the Activity tab) and press Retry.</div>}
      <div className="tabs sitetabs">{visible.map((t) => <span key={t} className={t === tab ? 'on' : ''} onClick={() => setSp(t === 'overview' ? {} : { tab: t })}>{tabLabel[t]}</span>)}</div>
      {tab === 'analytics' && <AnalyticsTab s={s} />}
      {tab === 'overview' && <Overview d={d.data} reload={d.reload} startJob={startJob} />}
      {tab === 'settings' && <SettingsTab s={s} sftp={d.data.sftp_available} onSaved={(j) => { d.reload(); if (j) setJob(j); }} />}
      {tab === 'files' && <FilesTab s={s} />}
      {tab === 'database' && s.db && <DatabaseTab s={s} />}
      {tab === 'wordpress' && <WordPressTab s={s} startJob={startJob} reload={d.reload} jobsDone={jobsDone} />}
      {tab === 'backups' && <BackupsTab s={s} backups={d.data.backups} reload={d.reload} startJob={startJob} />}
      {tab === 'logs' && <LogsTab s={s} />}
      {tab === 'activity' && <ActivityTab d={d.data} open={setJob} />}
      {job !== null && <Modal title="Job" onClose={() => { setJob(null); d.reload(); }}><JobView id={job} onDone={() => { d.reload(); setJobsDone((n) => n + 1); }} /></Modal>}
    </>
  );
}

function Overview({ d, reload, startJob }: { d: SiteDetail; reload: () => void; startJob: (p: Promise<{ job_id: number }>, m?: string) => void }) {
  const s = d.site;
  const toast = useToast();
  const [dbpw, setDbpw] = useState(false);
  const [sftpPw, setSftpPw] = useState('');
  const act = (p: Promise<unknown>, ok: string) => p.then(() => { toast(ok); reload(); }).catch((e) => toast(e.message, true));
  return (
    <div className="grid2" style={{ alignItems: 'start' }}>
      <div className="stack">
        <div className="card"><div className="ch"><h3>Site</h3></div><div className="cb kv">
          <div>Address</div><div><a href={siteURL(s)} target="_blank" rel="noreferrer">{siteURL(s)}</a></div>
          {(s.aliases?.length ?? 0) > 0 && <><div>Other domains</div><div>{s.aliases!.join(', ')}{s.redirect_aliases && <span className="dim"> (redirected)</span>}</div></>}
          <div>Type</div><div>{typeLabel[s.type]}</div>
          <div>PHP</div><div>{s.php_version} · {s.memory_mb} MB memory · {s.upload_mb} MB uploads · {s.max_children} workers</div>
          <div>Created</div><div>{fmtDate(s.created_at)}</div>
          <div>Disk</div><div>{bytes(d.usage.files)} files{s.db ? ' · ' + bytes(d.usage.db) + ' database' : ''}</div>
          <div>Last backup</div><div>{d.backups[0] ? ago(d.backups[0].time) : 'none'}{!s.backups && <span className="dim"> (nightly off)</span>}</div>
          {s.notes && <><div>Notes</div><div style={{ whiteSpace: 'pre-wrap' }}>{s.notes}</div></>}
        </div></div>
        {d.cloudflare && <CloudflareDNSCard site={s.id} />}
        {d.dns && <SiteDNSCard site={s.id} />}
        {localTLS(s) && <CertCard s={s} startJob={startJob} />}
        <div className="card"><div className="ch"><h3>Files</h3>
          <button className="btn s" onClick={() => act(post('/sites/' + s.id + '/fix-permissions'), 'Ownership fixed')}>Fix permissions</button></div>
          <div className="cb kv">
            <div>Folder</div><div className="mono">{d.dir}</div>
            <div>Web root</div><div className="mono">{d.dir}/public{s.doc_root ? '/' + s.doc_root : ''}</div>
            <div>System user</div><div className="mono">{d.user}</div>
            <div>PHP socket</div><div className="mono">{d.socket}</div>
          </div>
          <div className="cb dim" style={{ paddingTop: 0, fontSize: 12 }}>Copied files in as root? Press Fix permissions so PHP can write where it needs to.</div>
        </div>
      </div>
      <div className="stack">
        {s.db && <div className="card"><div className="ch"><h3>Database</h3>
          <button className="btn s" onClick={() => confirm('Set a new database password?' + (s.type === 'wordpress' ? ' wp-config.php is updated for you.' : ' Update your app\'s config afterwards.')) && act(post('/sites/' + s.id + '/db/password'), 'Password changed')}>New password</button></div>
          <div className="cb">
            <Field label="Host"><Copyable value="localhost" /></Field>
            <div className="grid2"><Field label="Database"><Copyable value={s.db.name} /></Field><Field label="User"><Copyable value={s.db.user} /></Field></div>
            <Field label="Password">{dbpw ? <Copyable value={s.db.password ?? ''} /> : <button className="btn s" onClick={() => setDbpw(true)}><Icon name="key" size={13} />Show</button>}</Field>
          </div></div>}
        {d.sftp_available && <div className="card"><div className="ch"><h3>SFTP</h3>{s.sftp.enabled ? <Badge kind="g">On</Badge> : <Badge kind="n">Off</Badge>}</div><div className="cb">
          {!s.sftp.enabled ? <p className="muted">Turn on SFTP in Settings to upload files with an SFTP client. Logins are locked inside this site's folder.</p> : <>
            <div className="grid2"><Field label="Server"><Copyable value={window.location.hostname} /></Field><Field label="User"><Copyable value={d.user} /></Field></div>
            <div className="dim" style={{ fontSize: 12, marginBottom: 12 }}>Port 22. Upload into <span className="mono">public</span>. {(s.sftp.keys?.length ?? 0)} SSH key{s.sftp.keys?.length === 1 ? '' : 's'}; password {s.sftp.password_set ? 'set' : 'not set'}.</div>
            {sftpPw ? <Field label="New password (shown once)"><Copyable value={sftpPw} /></Field> :
              <button className="btn s" onClick={() => post<{ password: string }>('/sites/' + s.id + '/sftp/password').then((r) => { setSftpPw(r.password); reload(); }).catch((e) => toast(e.message, true))}><Icon name="key" size={13} />{s.sftp.password_set ? 'Set a new password' : 'Create a password'}</button>}
          </>}
        </div></div>}
        <div className="card"><div className="ch"><h3>Backups</h3>
          <button className="btn s" disabled={s.status !== 'active'} onClick={() => startJob(post('/sites/' + s.id + '/backups'), 'Backup started')}><Icon name="backup" size={13} />Back up now</button></div>
          {d.backups.length === 0 ? <div className="empty">No backups yet.</div> : <table><tbody>{d.backups.slice(0, 3).map((b) => (
            <tr key={b.file}><td className="mono" style={{ fontSize: 12 }}>{b.file}</td><td className="muted">{bytes(b.size)}</td><td className="muted" style={{ textAlign: 'right' }}>{ago(b.time)}</td></tr>
          ))}</tbody></table>}
        </div>
      </div>
    </div>
  );
}

function SettingsTab({ s, sftp, onSaved }: { s: Site; sftp: boolean; onSaved: (job: number) => void }) {
  const { isAdmin } = useMe();
  const cfg = useLoad(() => get<{ installed_php: string[] }>('/settings'));
  const [f, setF] = useState<Site>(s);
  const [keys, setKeys] = useState((s.sftp.keys ?? []).join('\n'));
  const [replace, setReplace] = useState(true);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [delOpen, setDelOpen] = useState(false);
  const toast = useToast();
  useEffect(() => { setF(s); setKeys((s.sftp.keys ?? []).join('\n')); }, [s.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const set = (p: Partial<Site>) => setF({ ...f, ...p });
  const urlChanged = siteURL(f) !== siteURL(s);
  const save = async () => {
    setErr(null); setBusy(true);
    try {
      const body = { ...f, sftp: { ...f.sftp, keys: keys.split('\n').map((k) => k.trim()).filter(Boolean) }, replace_urls: replace };
      const r = await put<{ job_id: number }>('/sites/' + s.id, body);
      toast('Saved and applied');
      onSaved(r.job_id);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const busySite = s.status === 'provisioning' || s.status === 'deleting';
  return (
    <div style={{ maxWidth: 860 }}>
      <ErrorBox error={err} />
      <div className="card"><div className="ch"><h3>Domains</h3></div><div className="cb">
        <Field label="Main domain"><input className="input" value={f.domain} onChange={(e) => set({ domain: e.target.value.trim().toLowerCase() })} /></Field>
        <Field label="Other domains"><Chips values={f.aliases ?? []} onChange={(v) => set({ aliases: v })} placeholder="www.example.com" mono /></Field>
        {(f.aliases?.length ?? 0) > 0 && <Toggle on={f.redirect_aliases} onChange={(v) => set({ redirect_aliases: v })} label="Redirect other domains to the main domain" />}
        <Field label="Visitors use">
          <select className="input" value={serveOf(f)} onChange={(e) => set(serveFields(e.target.value as Serve))}>
            <option value="local">HTTPS (free Let's Encrypt certificate)</option>
            {(isAdmin || serveOf(f) === 'proxy') && <option value="proxy">HTTPS (a proxy or Cloudflare Tunnel in front ends it)</option>}
            <option value="http">HTTP only</option>
          </select>
        </Field>
        {urlChanged && s.type === 'wordpress' && <Toggle on={replace} onChange={setReplace} label={`Change ${siteURL(s)} to ${siteURL(f)} in the WordPress database`} help="Runs wp search-replace and updates the home and site URLs. Leave on unless you are doing it yourself." />}
      </div></div>
      <div className="card" style={{ marginTop: 16 }}><div className="ch"><h3>PHP</h3></div><div className="cb">
        <div className="grid2">
          <Field label="PHP version"><select className="input" value={f.php_version} onChange={(e) => set({ php_version: e.target.value })}>
            {(cfg.data?.installed_php ?? [f.php_version]).map((v) => <option key={v} value={v}>PHP {v}</option>)}</select></Field>
          <Field label="Workers" help={isAdmin ? 'Most PHP requests this site runs at once.' : 'Set by your hosting plan.'}><input className="input" type="number" min={1} max={200} disabled={!isAdmin} value={f.max_children} onChange={(e) => set({ max_children: +e.target.value })} /></Field>
          <Field label="Memory limit (MB)"><input className="input" type="number" min={32} disabled={!isAdmin} value={f.memory_mb} onChange={(e) => set({ memory_mb: +e.target.value })} /></Field>
          <Field label="Upload limit (MB)"><input className="input" type="number" min={1} value={f.upload_mb} onChange={(e) => set({ upload_mb: +e.target.value })} /></Field>
        </div>
        <div className="grid3">
          <Field label="Max execution time (s)"><input className="input" type="number" min={1} max={3600} value={f.php?.max_execution || 300} onChange={(e) => set({ php: { ...f.php, max_execution: +e.target.value } })} /></Field>
          <Field label="Max input vars"><input className="input" type="number" min={1} value={f.php?.max_input_vars || 5000} onChange={(e) => set({ php: { ...f.php, max_input_vars: +e.target.value } })} /></Field>
          <Field label="Max input time (s)" help="-1 uses the execution time."><input className="input" type="number" min={-1} max={3600} value={f.php?.max_input_time || 300} onChange={(e) => set({ php: { ...f.php, max_input_time: +e.target.value } })} /></Field>
        </div>
        {isAdmin && <Field label="More php.ini settings" help="One key = value per line, such as date.timezone = Europe/London or opcache.revalidate_freq = 0. Applied to this site's PHP only; PHP checks them before they go live.">
          <textarea className="input mono" style={{ minHeight: 90 }} spellCheck={false} value={f.php?.custom ?? ''} placeholder={'date.timezone = Europe/London\nmax_file_uploads = 50'} onChange={(e) => set({ php: { ...f.php, custom: e.target.value } })} />
        </Field>}
        {s.type !== 'wordpress' && <>
          <Field label="Document root inside public" help="Leave empty to serve public itself."><input className="input mono" value={f.doc_root} onChange={(e) => set({ doc_root: e.target.value })} /></Field>
          <Toggle on={f.front_controller} onChange={(v) => set({ front_controller: v })} label="Send unknown paths to index.php" />
        </>}
      </div></div>
      <div className="card" style={{ marginTop: 16 }}><div className="ch"><h3>Access and backups</h3></div><div className="cb">
        <Toggle on={f.enabled} onChange={(v) => set({ enabled: v })} label="Site is online" help="When off, visitors get a 503 page. Files and database are kept." />
        <Toggle on={f.backups} onChange={(v) => set({ backups: v })} label="Include in nightly backups" />
        <Toggle on={!f.no_monitor} onChange={(v) => set({ no_monitor: !v })} label="Uptime checks" help="Request the site every minute and log when it goes down. See Analytics → Uptime." />
        {sftp && <>
          <Toggle on={f.sftp.enabled} onChange={(v) => set({ sftp: { ...f.sftp, enabled: v } })} label="SFTP access" help="Logins only see this site's folder. Set a password on the Overview tab, or add SSH keys here." />
          {f.sftp.enabled && <Field label="SSH public keys, one per line"><textarea className="input" value={keys} onChange={(e) => setKeys(e.target.value)} placeholder="ssh-ed25519 AAAA… you@laptop" /></Field>}
        </>}
        <Field label="Notes"><textarea className="input" style={{ fontFamily: 'inherit', fontSize: 13 }} value={f.notes} onChange={(e) => set({ notes: e.target.value })} /></Field>
      </div></div>
      <div className="row" style={{ marginTop: 16, justifyContent: 'space-between' }}>
        <button className="btn p" disabled={busy || busySite} onClick={save}>Save changes</button>
        <button className="btn danger" disabled={busySite} onClick={() => setDelOpen(true)}><Icon name="trash" size={13} />Delete site</button>
      </div>
      {isAdmin && <OwnerCard s={s} />}
      {isAdmin && <NginxCard s={s} />}
      {delOpen && <DeleteModal s={s} onClose={() => setDelOpen(false)} />}
    </div>
  );
}

// OwnerCard lets an admin give the site to another account.
function OwnerCard({ s }: { s: Site }) {
  const accounts = useLoad(() => get<AccountView[]>('/accounts'));
  const [acct, setAcct] = useState(s.account_id);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  if (!accounts.data || accounts.data.length < 2) return null;
  const move = () => {
    const to = accounts.data!.find((a) => a.id === acct);
    if (!to || !confirm(`Give ${s.domain} to ${to.name}? Their logins will see and manage it.`)) return;
    put('/sites/' + s.id + '/account', { account_id: acct }).then(() => toast('Site moved to ' + to.name)).catch(setErr);
  };
  return (
    <div className="card" style={{ marginTop: 24 }}><div className="ch"><h3>Owner</h3></div><div className="cb">
      <ErrorBox error={err} />
      <div className="row" style={{ alignItems: 'flex-end' }}>
        <Field label="Account" style={{ flex: 1, marginBottom: 0 }}><select className="input" value={acct} onChange={(e) => setAcct(e.target.value)}>
          {accounts.data.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></Field>
        <button className="btn" disabled={acct === s.account_id} onClick={move}>Move site</button>
      </div>
    </div></div>
  );
}

function NginxCard({ s }: { s: Site }) {
  const d = useLoad(() => get<{ config: string; custom: string }>(`/sites/${s.id}/nginx`), [s.id]);
  const [custom, setCustom] = useState<string | null>(null);
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  const value = custom ?? d.data?.custom ?? '';
  const changed = custom !== null && custom !== (d.data?.custom ?? '');
  const save = () => {
    setBusy(true); setErr(null);
    put<{ config: string; custom: string }>(`/sites/${s.id}/nginx`, { custom: value })
      .then((r) => { d.setData(r); setCustom(null); toast('nginx config applied'); })
      .catch(setErr).finally(() => setBusy(false));
  };
  return (
    <div className="card" style={{ marginTop: 24 }}>
      <div className="ch"><h3>nginx config</h3><button className="btn s ghost" onClick={() => setShow(!show)}>{show ? 'Hide' : 'Show'} full config</button></div>
      <div className="cb">
        <p className="muted" style={{ marginBottom: 10, fontSize: 13 }}>Berth writes this site's server block. Add your own directives here, such as redirects, headers or extra <span className="mono">location</span> blocks. They go inside the server block, before Berth's locations, and survive every change Berth makes. nginx checks them before they go live; if it rejects them, nothing changes.</p>
        {d.error ? <ErrorBox error={d.error} /> : null}
        <textarea className="editor" style={{ minHeight: 180 }} spellCheck={false} value={value} placeholder={'location = /old-page {\n    return 301 /new-page;\n}\nadd_header X-Frame-Options SAMEORIGIN always;'}
          onChange={(e) => setCustom(e.target.value)}
          onKeyDown={(e) => { if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); if (changed) save(); } }} />
        <ErrorBox error={err} />
        <div style={{ display: 'flex', gap: 8, marginTop: 12, alignItems: 'center' }}>
          <button className="btn p" disabled={!changed || busy} onClick={save}>{busy ? 'Checking…' : 'Check and apply'}</button>
          {changed && <button className="btn ghost" onClick={() => { setCustom(null); setErr(null); }}>Discard</button>}
        </div>
        {show && d.data && <pre className="mono" style={{ marginTop: 16, fontSize: 12, whiteSpace: 'pre', overflowX: 'auto', background: 'var(--bg)', padding: 12, borderRadius: 8, maxHeight: 520 }}>{d.data.config}</pre>}
      </div>
    </div>
  );
}

function DeleteModal({ s, onClose }: { s: Site; onClose: () => void }) {
  const [typed, setTyped] = useState('');
  const [backup, setBackup] = useState(true);
  const [job, setJob] = useState<number | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const nav = useNavigate();
  return (
    <Modal title={'Delete ' + s.domain} onClose={job ? () => nav('/sites') : onClose}>
      {job ? <JobView id={job} onDone={(j: Job) => j.status === 'ok' && setTimeout(() => nav('/sites'), 1500)} /> : <>
        <ErrorBox error={err} />
        <p style={{ marginBottom: 12 }}>This removes the site's files{s.db ? ', database' : ''}, PHP pool, nginx config and system user.</p>
        <Toggle on={backup} onChange={setBackup} label="Keep a final backup first" help="Final backups are never pruned. Delete them from the Backups page when you no longer need them." />
        <Field label={`Type ${s.domain} to confirm`}><input className="input" value={typed} onChange={(e) => setTyped(e.target.value)} /></Field>
        <button className="btn danger" disabled={typed !== s.domain} onClick={() => del<{ job_id: number }>('/sites/' + s.id + '?backup=' + (backup ? 1 : 0)).then((r) => setJob(r.job_id)).catch(setErr)}>Delete site</button>
      </>}
    </Modal>
  );
}

function WordPressTab({ s, startJob, reload, jobsDone }: { s: Site; startJob: (p: Promise<{ job_id: number }>, m?: string) => void; reload: () => void; jobsDone: number }) {
  // Re-read versions after every job (such as an update) finishes.
  const info = useLoad(() => get<WPInfo>('/sites/' + s.id + '/wp'), [s.id, jobsDone]);
  const [rollback, setRollback] = useState(true);
  const [user, setUser] = useState('');
  const [pw, setPw] = useState<{ user: string; password: string; login_url: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  if (s.status !== 'active') return <div className="empty">WordPress tools are available once the site is live.</div>;
  const w = info.data;
  const updates = w ? (w.core_update ? 1 : 0) + w.plugin_updates + w.theme_updates : 0;
  return (
    <div className="grid2" style={{ alignItems: 'start' }}>
      <div className="stack">
        <div className="card"><div className="ch"><h3>WordPress</h3>
          {w && <button className="btn s" disabled={info.loading} onClick={info.reload}><Icon name="sync" size={12} />{info.loading ? 'Checking…' : 'Check again'}</button>}</div>
          {!w ? (info.error ? <div className="cb"><ErrorBox error={info.error} /></div> : <Loading />) : w.error ? <div className="cb"><div className="err">{w.error}</div></div> : <>
            <div className="cb kv">
              <div>Version</div><div>{w.version} {w.core_update ? <Badge kind="w">{w.core_update} available</Badge> : <Badge kind="g">Up to date</Badge>}</div>
              <div>Site address</div><div className="mono">{w.siteurl}</div>
              <div>Plugins</div><div>{w.plugins?.length ?? 0}{w.plugin_updates > 0 && <> · <Badge kind="w">{w.plugin_updates} to update</Badge></>}</div>
              <div>Themes</div><div>{w.themes?.length ?? 0}{w.theme_updates > 0 && <> · <Badge kind="w">{w.theme_updates} to update</Badge></>}</div>
            </div>
            <div className="cb" style={{ borderTop: '1px solid var(--line)' }}>
              <Toggle on={rollback} onChange={setRollback} label="Roll back if the site breaks" help="Checks the home page and login page after updating, and restores the backup if they fail." />
              <button className="btn p" onClick={() => startJob(post('/sites/' + s.id + '/wp/update?rollback=' + (rollback ? 1 : 0)), 'Updating WordPress')}><Icon name="sync" size={13} />{updates ? `Update everything (${updates})` : 'Run updates'}</button>
              <small className="help">Backs up the site first, then updates core, plugins, themes and translations.</small>
            </div>
          </>}
        </div>
        <AutoUpdateCard s={s} onSaved={reload} />
        <VulnCard s={s} />
        <div className="card"><div className="ch"><h3>Reset a login</h3></div><div className="cb">
          <ErrorBox error={err} />
          {pw ? <>
            <Field label="User"><Copyable value={pw.user} /></Field>
            <Field label="New password (shown once)"><Copyable value={pw.password} /></Field>
            <a className="btn" href={pw.login_url} target="_blank" rel="noreferrer"><Icon name="ext" size={13} />Open WP admin</a>
          </> : <>
            <Field label="WordPress username" help={`Leave empty for ${s.wp?.admin_user ?? 'the admin created with the site'}.`}><input className="input" value={user} onChange={(e) => setUser(e.target.value)} placeholder={s.wp?.admin_user} /></Field>
            <button className="btn" onClick={() => post('/sites/' + s.id + '/wp/admin-password', { user }).then(setPw).catch(setErr)}><Icon name="key" size={13} />Set a new password</button>
          </>}
        </div></div>
      </div>
      <div className="stack">
      <SearchReplaceCard s={s} startJob={startJob} />
      {w && !w.error && <div className="card"><div className="ch"><h3>Plugins and themes</h3></div>
        <table><thead><tr><th>Name</th><th>Version</th><th>Status</th></tr></thead><tbody>
          {[...(w.plugins ?? []).map((p) => ({ ...p, kind: 'plugin' })), ...(w.themes ?? []).map((p) => ({ ...p, kind: 'theme' }))].map((p) => (
            <tr key={p.kind + p.name}><td><b>{p.title || p.name}</b><div className="dim" style={{ fontSize: 11 }}>{p.kind}</div></td>
              <td className="mono">{p.version}{p.update && <div style={{ color: 'var(--warn)' }}>→ {p.update}</div>}</td>
              <td>{p.status === 'active' ? <Badge kind="g">Active</Badge> : <Badge kind="n">{p.status}</Badge>}</td></tr>
          ))}
          {(w.plugins?.length ?? 0) + (w.themes?.length ?? 0) === 0 && <tr><td colSpan={3} className="muted">None installed.</td></tr>}
        </tbody></table>
      </div>}
      </div>
    </div>
  );
}

export function BackupTable({ backups, site, reload, startJob, showSite }: { backups: Backup[]; site?: Site; reload: () => void; startJob: (p: Promise<{ job_id: number }>, m?: string) => void; showSite?: boolean }) {
  const toast = useToast();
  const [restore, setRestore] = useState<Backup | null>(null);
  const [browse, setBrowse] = useState<Backup | null>(null);
  if (backups.length === 0) return <div className="empty">No backups yet.</div>;
  return (
    <>
      <table>
        <thead><tr>{showSite && <th>Site</th>}<th>Backup</th><th className="hide-sm">Size</th><th>Taken</th><th className="hide-sm">Where</th><th></th></tr></thead>
        <tbody>{backups.map((b) => (
          <tr key={b.site + b.file}>
            {showSite && <td><Link to={'/sites/' + b.site + '?tab=backups'}>{b.site}</Link></td>}
            <td className="mono" style={{ fontSize: 12 }}>{b.file}{b.file.endsWith('-final.tar.gz') && <> <Badge kind="w">final</Badge></>}</td>
            <td className="muted hide-sm">{bytes(b.size)}</td>
            <td className="muted">{fmtDate(b.time)}</td>
            <td className="hide-sm"><Where b={b} /></td>
            <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
              {site && site.status === 'active' && <button className="btn s" onClick={() => setRestore(b)}><Icon name="hist" size={13} />Restore</button>}
              {site && <button className="btn s ghost" title="Browse files" onClick={() => setBrowse(b)}><Icon name="folder" size={13} /></button>}
              <a className="btn s ghost" href={`/api/sites/${b.site}/backups/${b.file}`} title="Download"><Icon name="down" size={13} /></a>
              <button className="btn s ghost" title="Delete" onClick={() => confirm(`Delete ${b.file}${b.remote ? ' here and from off-site storage' : ''}?`) && del(`/sites/${b.site}/backups/${b.file}`).then(() => { toast('Backup deleted'); reload(); }).catch((e) => toast(e.message, true))}><Icon name="trash" size={13} /></button>
            </td>
          </tr>
        ))}</tbody>
      </table>
      {site && restore && <RestoreModal site={site} b={restore} onClose={() => setRestore(null)} startJob={startJob} />}
      {site && browse && <BackupBrowser site={site} b={browse} onClose={() => setBrowse(null)} startJob={startJob} />}
    </>
  );
}

function BackupsTab({ s, backups, reload, startJob }: { s: Site; backups: Backup[]; reload: () => void; startJob: (p: Promise<{ job_id: number }>, m?: string) => void }) {
  return (
    <div className="card">
      <div className="ch"><h3>Backups of {s.domain}</h3>
        <button className="btn s p" disabled={s.status !== 'active'} onClick={() => startJob(post('/sites/' + s.id + '/backups'), 'Backup started')}><Icon name="backup" size={13} />Back up now</button></div>
      <BackupTable backups={backups} site={s} reload={reload} startJob={startJob} />
      <div className="cb dim" style={{ fontSize: 12, borderTop: '1px solid var(--line)' }}>Each backup is one .tar.gz with the site's files, {s.db ? 'a database dump, ' : ''}and its settings. Restore brings back everything, only the files or only the database, after backing up the current state. Browse a backup to download or put back single files and folders.</div>
    </div>
  );
}

function LogsTab({ s }: { s: Site }) {
  const [file, setFile] = useState<'access' | 'error' | 'php'>('access');
  const logs = useLoad(() => get<{ path: string; lines: string[] }>(`/sites/${s.id}/logs?file=${file}&lines=400`), [s.id, file]);
  return (
    <div className="card">
      <div className="ch">
        <div className="seg">
          {(['access', 'error', 'php'] as const).map((f) => <span key={f} className={file === f ? 'on' : ''} onClick={() => setFile(f)}>{f === 'access' ? 'Access' : f === 'error' ? 'nginx errors' : 'PHP errors'}</span>)}
        </div>
        <button className="btn s" onClick={logs.reload}><Icon name="sync" size={12} />Refresh</button>
      </div>
      <div className="cb">
        {!logs.data ? <Loading /> : <>
          <div className="dim mono" style={{ marginBottom: 8, fontSize: 11 }}>{logs.data.path}</div>
          {logs.data.lines.length === 0 ? <div className="empty">Empty.</div> : <div className="code logview mono">{logs.data.lines.join('\n')}</div>}
        </>}
      </div>
    </div>
  );
}

function ActivityTab({ d, open }: { d: SiteDetail; open: (id: number) => void }) {
  return (
    <div className="grid2" style={{ alignItems: 'start' }}>
      <div className="card"><div className="ch"><h3>Jobs</h3></div>
        {d.jobs.length === 0 ? <div className="empty">No jobs.</div> : <table><tbody>{d.jobs.map((j) => (
          <tr key={j.id} className="click" onClick={() => open(j.id)}><td style={{ whiteSpace: 'normal', wordBreak: 'break-word' }}>{j.title}<div className="dim" style={{ fontSize: 12 }}>{j.error.split('\n')[0]}</div></td><td className="muted">{ago(j.started)}</td><td style={{ textAlign: 'right' }}><JobBadge status={j.status} /></td></tr>
        ))}</tbody></table>}
      </div>
      <div className="card"><div className="ch"><h3>Events</h3></div>
        {d.events.length === 0 ? <div className="empty">No events.</div> : <table><tbody>{d.events.map((e) => (
          <tr key={e.id}><td style={{ whiteSpace: 'normal' }}>{e.message}</td><td className="muted" style={{ textAlign: 'right' }}>{ago(e.time)}</td></tr>
        ))}</tbody></table>}
      </div>
    </div>
  );
}

// WPAdminButton opens WP admin already logged in, through a single-use link.
// The tab opens straight away (so pop-up blockers allow it) and goes to the
// link once Berth has made it.
function WPAdminButton({ site }: { site: Site }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const open = async () => {
    const w = window.open('', '_blank');
    setBusy(true);
    try {
      const r = await post<{ url: string; user: string }>('/sites/' + site.id + '/wp/login');
      if (w) { w.opener = null; w.location.href = r.url; } else window.location.href = r.url;
    } catch (e) {
      w?.close();
      toast((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  };
  return <button className="btn white" onClick={open} disabled={busy} title="Opens WP admin logged in, no password needed"><Icon name="wp" />{busy ? 'Opening…' : 'WP admin'}</button>;
}

const localTLS = (s: Site) => s.https && s.tls_mode === 'local';

function httpsLabel(s: Site) {
  if (!s.https) return 'Off';
  if (!localTLS(s)) return 'On';
  switch (s.tls?.status) {
    case 'active': return 'On';
    case 'error': return 'Failing';
    default: return 'Waiting';
  }
}

// CertCard shows the site's Let's Encrypt certificate.
function CertCard({ s, startJob }: { s: Site; startJob: (p: Promise<{ job_id: number }>, m?: string) => void }) {
  const t = s.tls ?? { status: '' };
  const days = t.expires ? Math.floor((new Date(t.expires).getTime() - Date.now()) / 86400000) : 0;
  const badge = t.status === 'active' ? <Badge kind="g">Valid</Badge> : t.status === 'error' ? <Badge kind="b">Failed</Badge> : <Badge kind="n">Not issued yet</Badge>;
  return (
    <div className="card"><div className="ch"><h3>HTTPS certificate</h3>{badge}</div><div className="cb">
      {t.status === 'active' ? <div className="kv">
        <div>Issuer</div><div>Let's Encrypt</div>
        <div>Covers</div><div className="mono" style={{ fontSize: 12.5 }}>{(t.domains ?? []).join(', ')}</div>
        <div>Expires</div><div>{fmtDate(t.expires!)} <span className="dim">({days} day{days === 1 ? '' : 's'}; renews 30 days before)</span></div>
      </div> : <p className="muted" style={{ marginBottom: 12 }}>
        {t.status === 'error' ? <>The last request failed: <span className="mono" style={{ fontSize: 12.5 }}>{t.error}</span></> : 'Berth requests a free certificate once the site is live.'}
        {' '}The domains must point at this server, with ports 80 and 443 open.
        {t.next_try && t.status === 'error' && <> Berth tries again {fmtDate(t.next_try)}.</>}
      </p>}
      {s.status === 'active' && <button className="btn s" style={{ marginTop: t.status === 'active' ? 12 : 0 }} onClick={() => startJob(post('/sites/' + s.id + '/tls'), 'Requesting a certificate')}>
        <Icon name="sync" />{t.status === 'active' ? 'Renew now' : 'Request now'}</button>}
    </div></div>
  );
}
