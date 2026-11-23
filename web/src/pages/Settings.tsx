import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { APIToken, del, exposureLabel, get, HalyardInfo, post, put, putFile, Settings as S, vipLabel } from '../api';
import { Badge, BrandMark, Chips, Copyable, ErrorBox, Field, Loading, Toggle, useLoad, useToast } from '../ui';
import type { Me } from '../me';

interface SettingsView { settings: S; halyard_token_set: boolean; s3_secret_set: boolean; s3_error: string; installed_php: string[]; php_versions: string[] }

// HalyardPage is the Halyard plugin's page: the connection to the load
// balancer and the defaults for publishing sites.
export function HalyardPage() {
  const { data, error, reload } = useLoad(() => get<SettingsView>('/settings'));
  const [f, setF] = useState<S | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  useEffect(() => { if (data) setF(data.settings); }, [data]);
  if (!data || !f) return error ? <ErrorBox error={error} /> : <Loading />;
  const set = (p: Partial<S>) => setF({ ...f, ...p });
  const save = async () => {
    setErr(null); setBusy(true);
    try { await put('/settings', f); toast('Halyard settings saved'); reload(); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <>
      <div className="head"><div><h1>Halyard</h1><div className="sub">Publish sites on your load balancer. Only sites of the accounts this plugin is on for (see <Link to="/plugins">Plugins</Link>) can be published.</div></div>
        <button className="btn p" disabled={busy} onClick={save}>Save</button></div>
      <ErrorBox error={err} />
      <div style={{ maxWidth: 860 }}><HalyardCard f={f} set={set} tokenSet={data.halyard_token_set} saved={data.settings} onSaved={reload} /></div>
    </>
  );
}

export default function Settings({ me }: { me: Me }) {
  const { data, error, reload } = useLoad(() => get<SettingsView>('/settings'));
  const [f, setF] = useState<S | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  useEffect(() => { if (data) setF(data.settings); }, [data]);
  if (!data || !f) return error ? <ErrorBox error={error} /> : <Loading />;
  const set = (p: Partial<S>) => setF({ ...f, ...p });
  const save = async () => {
    setErr(null); setBusy(true);
    try { await put('/settings', f); toast('Settings saved and applied'); reload(); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <>
      <div className="head"><div><h1>Settings</h1><div className="sub">How sites sit behind your load balancer, defaults for new sites, backups and your account.</div></div>
        <button className="btn p" disabled={busy} onClick={save}>Save settings</button></div>
      <ErrorBox error={err} />
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Behind the load balancer</h3></div><div className="cb">
            <Field label="Trusted proxies" help="Your Halyard nodes' addresses (or their subnet). Only these may tell sites the visitor's IP (X-Real-IP) and that the visitor used HTTPS (X-Forwarded-Proto). Anyone else's headers are ignored.">
              <Chips values={f.trusted_proxies ?? []} onChange={(v) => set({ trusted_proxies: v })} placeholder="10.0.30.11 or 10.0.30.0/24" mono />
            </Field>
            <Toggle on={f.proxy_only} onChange={(v) => set({ proxy_only: v })} label="Refuse visitors that skip the load balancer" help="Sites answer 403 unless the request comes from a trusted proxy (or this machine)." />
          </div></div>
          <div className="note">Halyard and Cloudflare are plugins now: set them up on the <Link to="/plugins">Plugins</Link> page and choose the accounts they are on for.</div>
          <div className="card"><div className="ch"><h3>New sites</h3></div><div className="cb">
            <Field label="Default PHP version"><select className="input" value={f.default_php} onChange={(e) => set({ default_php: e.target.value })}>
              {data.php_versions.map((v) => <option key={v} value={v} disabled={!data.installed_php.includes(v)}>PHP {v}{data.installed_php.includes(v) ? '' : ' (not installed)'}</option>)}</select></Field>
            <div className="grid3">
              <Field label="WordPress admin user"><input className="input" value={f.wp_admin_user} onChange={(e) => set({ wp_admin_user: e.target.value })} /></Field>
              <Field label="Admin email"><input className="input" value={f.wp_admin_email} onChange={(e) => set({ wp_admin_email: e.target.value })} /></Field>
              <Field label="Language"><input className="input mono" value={f.wp_locale} onChange={(e) => set({ wp_locale: e.target.value })} /></Field>
            </div>
          </div></div>
          <div className="card"><div className="ch"><h3>Backups</h3></div><div className="cb">
            <Toggle on={f.backup_enabled} onChange={(v) => set({ backup_enabled: v })} label="Nightly backups" help="Sites with backups on are saved one after another." />
            <div className="grid2">
              <Field label="Time (server time)"><input className="input mono" value={f.backup_time} onChange={(e) => set({ backup_time: e.target.value })} placeholder="02:30" /></Field>
              <Field label="Keep per site"><input className="input" type="number" min={1} value={f.backup_keep} onChange={(e) => set({ backup_keep: +e.target.value })} /></Field>
            </div>
            <Field label="Folder" help="Can be a NAS share mounted into the container."><input className="input mono" value={f.backup_dir} onChange={(e) => set({ backup_dir: e.target.value })} /></Field>
          </div></div>
          <S3Card f={f} set={set} secretSet={data.s3_secret_set} remoteError={data.s3_error} />
        </div>
        <div className="stack">
          <MonitoringCard f={f} set={set} />
          <ACMECard f={f} set={set} />
          <BrandCard f={f} set={set} />
          <TokensCard />
          <AccountCard me={me} />
        </div>
      </div>
    </>
  );
}

function MonitoringCard({ f, set }: { f: S; set: (p: Partial<S>) => void }) {
  return (
    <div className="card"><div className="ch"><h3>Monitoring</h3></div><div className="cb">
      <Toggle on={!f.no_uptime} onChange={(v) => set({ no_uptime: !v })} label="Uptime checks" help="Request every live site once a minute and log outages in Activity. Turn off single sites on their Settings tab." />
      <Toggle on={!f.no_vuln_scan} onChange={(v) => set({ no_vuln_scan: !v })} label="Daily vulnerability check" help="Compare WordPress, plugins and themes with the free WPVulnerability database once a day." />
    </div></div>
  );
}

function BrandCard({ f, set }: { f: S; set: (p: Partial<S>) => void }) {
  const b = f.brand ?? { name: '', support_email: '', support_url: '', ipv4: '', ipv6: '' };
  const setb = (p: Partial<typeof b>) => set({ brand: { ...b, ...p } });
  return (
    <div className="card"><div className="ch"><h3>Customers</h3></div><div className="cb">
      <p className="muted" style={{ marginBottom: 12 }}>What hosting customers see.</p>
      <Field label="Panel name" help="Replaces Berth in the sidebar, the sign-in page and the browser tab."><input className="input" value={b.name} placeholder="Berth" onChange={(e) => setb({ name: e.target.value })} /></Field>
      <div className="grid2">
        <Field label="Support email"><input className="input" value={b.support_email} onChange={(e) => setb({ support_email: e.target.value })} /></Field>
        <Field label="Support link"><input className="input" value={b.support_url} placeholder="https://" onChange={(e) => setb({ support_url: e.target.value })} /></Field>
        <Field label="Public IPv4" help="Customers point their domains here."><input className="input mono" value={b.ipv4} onChange={(e) => setb({ ipv4: e.target.value })} /></Field>
        <Field label="Public IPv6"><input className="input mono" value={b.ipv6} onChange={(e) => setb({ ipv6: e.target.value })} /></Field>
      </div>
      <div className="grid2">
        <BrandImage kind="logo" value={b.logo} name={b.name} onChange={(v) => setb({ logo: v })}
          help="Replaces the icon and panel name in the sidebar and on the sign-in page. PNG, JPEG, WebP or SVG, up to 1 MB. A wide logo about 180 by 32 pixels, light on dark, fits best." />
        <BrandImage kind="favicon" value={b.favicon} name={b.name} onChange={(v) => setb({ favicon: v })}
          help="The browser tab's icon. PNG, ICO or SVG, square, 32 by 32 pixels or larger." />
      </div>
    </div></div>
  );
}

// BrandImage uploads or removes the logo or favicon. It saves at once.
function BrandImage({ kind, value, name, onChange, help }: { kind: 'logo' | 'favicon'; value?: string; name: string; onChange: (v: string) => void; help: string }) {
  const toast = useToast();
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const file = useRef<HTMLInputElement>(null);
  const label = kind === 'logo' ? 'Logo' : 'Favicon';
  const run = async (fn: () => Promise<string>) => {
    setErr(null); setBusy(true);
    try {
      const v = await fn();
      onChange(v);
      window.dispatchEvent(new Event('berth-me'));
      toast(v ? label + ' updated' : label + ' removed');
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const pick = (file?: File) => file && run(async () => (await putFile<Record<string, string>>('/settings/brand/' + kind, file))[kind]);
  return (
    <Field label={label} help={help}>
      <ErrorBox error={err} />
      <div className={'brandpreview' + (kind === 'favicon' ? ' fav' : '')}>
        {kind === 'logo' ? <div className="brand" style={{ padding: 0 }}><BrandMark logo={value} name={name} /></div>
          : value ? <img src={'/api/brand/favicon?v=' + encodeURIComponent(value)} alt="" width={32} height={32} /> : <span className="muted">Berth's icon</span>}
      </div>
      <div className="row" style={{ gap: 8, marginTop: 8 }}>
        <button className="btn" disabled={busy} onClick={() => file.current?.click()}>{value ? 'Replace' : 'Upload'}</button>
        <input ref={file} type="file" hidden accept={kind === 'logo' ? 'image/png,image/jpeg,image/webp,image/gif,image/svg+xml' : 'image/png,image/x-icon,image/vnd.microsoft.icon,image/svg+xml,image/gif,.ico'}
            onChange={(e) => { pick(e.target.files?.[0]); e.target.value = ''; }} />
        {value && <button className="btn" disabled={busy} onClick={() => run(async () => { await del('/settings/brand/' + kind); return ''; })}>Use Berth's</button>}
      </div>
    </Field>
  );
}

// TokensCard manages API tokens for billing systems and scripts.
function TokensCard() {
  const { data, error, reload } = useLoad(() => get<APIToken[]>('/tokens'));
  const [name, setName] = useState('');
  const [scope, setScope] = useState<'provisioning' | 'full'>('provisioning');
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const create = async () => {
    setErr(null);
    try {
      const r = await post<{ name: string; token: string }>('/tokens', { name, scope });
      setCreated(r); setName(''); reload();
    } catch (e) { setErr(e); }
  };
  const revoke = async (t: APIToken) => {
    if (!window.confirm('Revoke the API token "' + t.name + '"? Anything using it stops working.')) return;
    setErr(null);
    try { await del('/tokens/' + t.id); reload(); } catch (e) { setErr(e); }
  };
  return (
    <div className="card"><div className="ch"><h3>API tokens</h3></div><div className="cb">
      <p className="muted" style={{ marginBottom: 12 }}>For a billing system (WHMCS, Blesta, your own) to create accounts on a plan, change plans, suspend and terminate them, and sign customers in. Send it as <span className="mono">Authorization: Bearer &lt;token&gt;</span>. See the README's "Provisioning API".</p>
      <ErrorBox error={err || error} />
      {created && <div className="note" style={{ marginBottom: 12 }}>
        <b>{created.name}</b>: copy the token now. It is not shown again.
        <div style={{ marginTop: 6 }}><Copyable value={created.token} /></div>
      </div>}
      {data && data.length > 0 && <table style={{ marginBottom: 12 }}>
        <thead><tr><th>Token</th><th>Access</th><th /></tr></thead>
        <tbody>{data.map((t) => <tr key={t.id}>
          <td><b>{t.name}</b><div className="muted" style={{ fontSize: 12 }}>Last used {t.last_used && !t.last_used.startsWith('0001') ? new Date(t.last_used).toLocaleString() : 'never'}</div></td>
          <td><Badge kind={t.scope === 'full' ? 'w' : 'n'}>{t.scope === 'full' ? 'Whole API' : 'Provisioning'}</Badge></td>
          <td style={{ textAlign: 'right' }}><button className="btn s" onClick={() => revoke(t)}>Revoke</button></td>
        </tr>)}</tbody>
      </table>}
      <div className="grid2">
        <Field label="New token name"><input className="input" value={name} placeholder="WHMCS" onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Access"><select className="input" value={scope} onChange={(e) => setScope(e.target.value as 'provisioning' | 'full')}>
          <option value="provisioning">Provisioning API only</option>
          <option value="full">Whole API (acts as you)</option>
        </select></Field>
      </div>
      <button className="btn p" disabled={!name.trim()} onClick={create}>Create token</button>
    </div></div>
  );
}

function ACMECard({ f, set }: { f: S; set: (p: Partial<S>) => void }) {
  const a = f.acme ?? { email: '', staging: false, off: false };
  const seta = (p: Partial<typeof a>) => set({ acme: { ...a, ...p } });
  return (
    <div className="card"><div className="ch"><h3>HTTPS certificates</h3></div><div className="cb">
      <p className="muted" style={{ marginBottom: 12 }}>Sites set to "HTTPS (free Let's Encrypt certificate)" get one automatically, renewed 30 days before it expires. Their domains must point at this server, with ports 80 and 443 open.</p>
      <Field label="Contact email" help="Let's Encrypt writes here if a certificate is about to expire. Blank uses the first admin's email."><input className="input" value={a.email} onChange={(e) => seta({ email: e.target.value })} /></Field>
      <Toggle on={a.staging} onChange={(v) => seta({ staging: v })} label="Use the staging server" help="For testing: browsers do not trust its certificates, but its rate limits are generous." />
      <Toggle on={!a.off} onChange={(v) => seta({ off: !v })} label="Request certificates" help="Off stops new requests and renewals. Existing certificates keep working until they expire." />
    </div></div>
  );
}

export function AccountCard({ me }: { me: Me }) {
  const toast = useToast();
  const [err, setErr] = useState<unknown>(null);
  const [pw, setPw] = useState({ current: '', new: '' });
  const [totp, setTotp] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState('');
  const [totpOn, setTotpOn] = useState(me.user.totp_enabled);
  const go = (fn: () => Promise<unknown>, ok: string) => async () => {
    setErr(null);
    try { await fn(); toast(ok); } catch (e) { setErr(e); }
  };
  return (
    <div className="card"><div className="ch"><h3>Your account</h3><span className="muted">{me.user.username}</span></div><div className="cb">
      <ErrorBox error={err} />
      <div className="grid2">
        <Field label="Current password"><input className="input" type="password" value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} /></Field>
        <Field label="New password"><input className="input" type="password" value={pw.new} onChange={(e) => setPw({ ...pw, new: e.target.value })} /></Field>
      </div>
      <button className="btn" onClick={go(() => post('/me/password', pw).then(() => setPw({ current: '', new: '' })), 'Password changed')}>Change password</button>
      <div style={{ borderTop: '1px solid var(--line)', margin: '18px -18px 16px' }} />
      <div className="row" style={{ alignItems: 'center', marginBottom: 12 }}><b>Two-factor sign-in</b>{totpOn ? <Badge kind="g">On</Badge> : <Badge kind="n">Off</Badge>}</div>
      {!totpOn && !totp && <button className="btn" onClick={go(async () => setTotp(await post('/me/totp')), 'Add the secret to your authenticator app')}>Set up</button>}
      {!totpOn && totp && <>
        <Field label="Secret for your authenticator app"><div className="input mono" style={{ userSelect: 'all' }}>{totp.secret}</div></Field>
        <Field label="Code from the app"><input className="input mono" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
        <button className="btn p" onClick={go(() => post('/me/totp/enable', { code }).then(() => { setTotpOn(true); setTotp(null); setCode(''); }), 'Two-factor is on')}>Turn on</button>
      </>}
      {totpOn && <>
        <Field label="Current code to turn off"><input className="input mono" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
        <button className="btn danger" onClick={go(() => post('/me/totp/disable', { code }).then(() => { setTotpOn(false); setCode(''); }), 'Two-factor is off')}>Turn off</button>
      </>}
      {me.user.role === 'admin' && <small className="help">Locked out? Run <span className="mono">berth passwd</span> in the container.</small>}
    </div></div>
  );
}

function HalyardCard({ f, set, tokenSet, saved, onSaved }: { f: S; set: (p: Partial<S>) => void; tokenSet: boolean; saved: S; onSaved: () => void }) {
  const h = f.halyard;
  const seth = (p: Partial<S['halyard']>) => set({ halyard: { ...h, ...p } });
  const { data: info, reload } = useLoad(() => get<HalyardInfo>('/halyard'), [saved]);
  const [err, setErr] = useState<unknown>(null);
  const [addr, setAddr] = useState('');
  const toast = useToast();
  const connected = !!saved.halyard.url && tokenSet;
  const createPool = async () => {
    setErr(null);
    try {
      const p = await post<{ id: string; name: string }>('/halyard/pool', { address: addr });
      toast(`Created pool ${p.name} on Halyard`); seth({ pool_id: p.id }); onSaved(); reload();
    } catch (e) { setErr(e); }
  };
  return (
    <div className="card"><div className="ch"><h3>Halyard</h3>
      {!connected ? <Badge kind="n">Not connected</Badge> : info?.error ? <Badge kind="b">Error</Badge> : info?.configured ? <Badge kind="g">Connected</Badge> : null}</div><div className="cb">
      <p className="muted" style={{ marginBottom: 12 }}>Connect Halyard and Berth adds a host on it for each site you publish, with a certificate when the site uses HTTPS. Create an API token on Halyard's Settings page.</p>
      <div className="grid2">
        <Field label="Halyard URL"><input className="input mono" value={h.url} onChange={(e) => seth({ url: e.target.value })} placeholder="https://10.0.30.11:8443" /></Field>
        <Field label="API token"><input className="input mono" type="password" value={h.token ?? ''} onChange={(e) => seth({ token: e.target.value })} placeholder={tokenSet ? 'saved (leave blank to keep)' : 'hlt_...'} /></Field>
      </div>
      <Toggle on={h.insecure} onChange={(v) => seth({ insecure: v })} label="Accept Halyard's self-signed certificate" help="Turn on when the URL is Halyard's own address rather than a name with a trusted certificate." />
      {info?.error && <div className="err" style={{ marginBottom: 12 }}>{info.error}</div>}
      {connected && info?.configured && !info.error && <>
        <Field label="Pool for this server" help="Every site's host sends traffic here. The pool health-checks /berth-health on port 80.">
          <select className="input" value={h.pool_id} onChange={(e) => seth({ pool_id: e.target.value })}>
            <option value="">Choose a pool</option>
            {info.pools.map((p) => <option key={p.id} value={p.id}>{p.name}{p.members?.length ? ` (${p.members.map((m) => m.address).join(', ')})` : ''}</option>)}
          </select>
        </Field>
        {!h.pool_id && <div className="note">
          <div style={{ marginBottom: 8 }}>No pool yet? Berth can create one that points at this server.</div>
          <div style={{ display: 'flex', gap: 8 }}>
            <input className="input mono" value={addr} onChange={(e) => setAddr(e.target.value)} placeholder={info.local_addr ? `${info.local_addr}:80` : '10.0.30.20:80'} />
            <button className="btn" onClick={createPool}>Create pool</button>
          </div>
        </div>}
        <div className="grid2">
          <Field label="Default VIP"><select className="input" value={h.vip_id} onChange={(e) => seth({ vip_id: e.target.value })}>
            <option value="">Choose a VIP</option>
            {info.vips.map((v) => <option key={v.id} value={v.id}>{vipLabel(v)}</option>)}</select></Field>
          <Field label="Default exposure"><select className="input" value={h.exposure || 'public'} onChange={(e) => seth({ exposure: e.target.value })}>
            {Object.entries(exposureLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
        </div>
        <Toggle on={!h.no_dns} onChange={(v) => seth({ no_dns: !v })} label="Manage DNS in Cloudflare" help="Halyard creates each site's records. Public and tunnel sites are proxied (orange cloud); LAN sites get a plain record to the VIP." />
        {!h.no_dns && info.cloudflare && !info.cloudflare.token_set && <div className="warnbox">Halyard has no Cloudflare API token yet, so it cannot create DNS records or certificates. Add one on Halyard's Cloudflare page.</div>}
        {!h.no_dns && info.cloudflare?.token_set && h.exposure === 'public' && !info.cloudflare.public_ip && <div className="warnbox">Set your public IP on Halyard's Cloudflare page; proxied records for public sites point at it.</div>}
        {!h.no_dns && info.cloudflare?.token_set && h.exposure === 'tunnel' && !info.cloudflare.tunnel_id && <div className="warnbox">Halyard has no Cloudflare Tunnel yet. Create one on its Cloudflare page.</div>}
        <Toggle on={h.auto_cert} onChange={(v) => seth({ auto_cert: v })} label="Request a certificate for HTTPS sites" help="Halyard issues one through Cloudflare DNS unless an existing certificate (a wildcard, say) already covers the site." />
      </>}
      {connected && !info && <Loading />}
      {!connected && <div className="note" style={{ marginBottom: 0 }}>Save the URL and token, then choose the pool and VIP here.</div>}
      <ErrorBox error={err} />
      <div className="note" style={{ marginBottom: 0, marginTop: 12 }}>Add Halyard's node addresses as trusted proxies on the <Link to="/settings">Settings</Link> page too, so sites see visitors' real addresses.</div>
    </div></div>
  );
}

function S3Card({ f, set, secretSet, remoteError }: { f: S; set: (p: Partial<S>) => void; secretSet: boolean; remoteError: string }) {
  const c = f.s3;
  const setc = (p: Partial<S['s3']>) => set({ s3: { ...c, ...p } });
  const [test, setTest] = useState<{ ok: boolean; msg: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const run = async () => {
    setBusy(true); setTest(null);
    try { await post('/settings/s3/test', { ...c, enabled: true }); setTest({ ok: true, msg: 'Berth could write to and delete from the bucket.' }); }
    catch (e) { setTest({ ok: false, msg: (e as Error).message }); } finally { setBusy(false); }
  };
  return (
    <div className="card"><div className="ch"><h3>Off-site backups (S3)</h3>{c.enabled ? (remoteError ? <Badge kind="b">Error</Badge> : <Badge kind="g">On</Badge>) : <Badge kind="n">Off</Badge>}</div><div className="cb">
      <Toggle on={c.enabled} onChange={(v) => setc({ enabled: v })} label="Copy every backup to S3-compatible storage" help="Backblaze B2, Cloudflare R2, Wasabi, AWS S3 or MinIO. Restores and browsing download from there when the local copy is gone." />
      {c.enabled && remoteError && <div className="err">{remoteError}</div>}
      <div className="grid2">
        <Field label="Endpoint" help="B2: the S3 endpoint on the bucket page."><input className="input mono" value={c.endpoint} onChange={(e) => setc({ endpoint: e.target.value })} placeholder="s3.eu-central-003.backblazeb2.com" /></Field>
        <Field label="Region"><input className="input mono" value={c.region} onChange={(e) => setc({ region: e.target.value })} placeholder="eu-central-003" /></Field>
        <Field label="Bucket"><input className="input mono" value={c.bucket} onChange={(e) => setc({ bucket: e.target.value })} placeholder="my-backups" /></Field>
        <Field label="Folder in bucket (optional)"><input className="input mono" value={c.prefix} onChange={(e) => setc({ prefix: e.target.value })} placeholder="berth" /></Field>
        <Field label="Key ID"><input className="input mono" value={c.access_key} onChange={(e) => setc({ access_key: e.target.value })} /></Field>
        <Field label="Application key (secret)"><input className="input mono" type="password" value={c.secret_key ?? ''} onChange={(e) => setc({ secret_key: e.target.value })} placeholder={secretSet ? 'saved (leave blank to keep)' : ''} /></Field>
        <Field label="Keep off-site per site"><input className="input" type="number" min={1} value={c.keep} onChange={(e) => setc({ keep: +e.target.value })} /></Field>
        <Field label="Keep locally per site" help="0 uses the backup setting above."><input className="input" type="number" min={0} value={c.local_keep} onChange={(e) => setc({ local_keep: +e.target.value })} /></Field>
      </div>
      <Toggle on={c.path_style} onChange={(v) => setc({ path_style: v })} label="Path-style URLs" help="Only for MinIO and similar self-hosted storage." />
      <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
        <button className="btn" disabled={busy || !c.endpoint || !c.bucket} onClick={run}>{busy ? 'Testing…' : 'Test connection'}</button>
        {test && <span style={{ fontSize: 12.5, color: test.ok ? 'var(--good)' : 'var(--bad)' }}>{test.msg}</span>}
      </div>
      <small className="help">Give the key access to this bucket only. Backups hold database passwords, so keep the bucket private.</small>
    </div></div>
  );
}
