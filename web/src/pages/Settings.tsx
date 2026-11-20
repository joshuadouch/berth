import { useEffect, useState } from 'react';
import { exposureLabel, get, HalyardInfo, post, put, Settings as S, vipLabel } from '../api';
import { Badge, Chips, ErrorBox, Field, Loading, Toggle, useLoad, useToast } from '../ui';
import type { Me } from '../App';

interface SettingsView { settings: S; halyard_token_set: boolean; installed_php: string[]; php_versions: string[] }

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
          <HalyardCard f={f} set={set} tokenSet={data.halyard_token_set} saved={data.settings} onSaved={reload} />
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
        </div>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Cloudflare Access</h3>{f.access_team && f.access_aud ? <Badge kind="g">On</Badge> : <Badge kind="n">Off</Badge>}</div><div className="cb">
            <p className="muted" style={{ marginBottom: 12 }}>Publish this panel through Cloudflare Access and you are signed in by Access instead of a password.</p>
            <div className="grid2">
              <Field label="Team name"><input className="input mono" value={f.access_team} onChange={(e) => set({ access_team: e.target.value })} placeholder="myteam" /></Field>
              <Field label="Application AUD tag"><input className="input mono" value={f.access_aud} onChange={(e) => set({ access_aud: e.target.value })} /></Field>
            </div>
            <Field label="Allowed emails" help={`Your account email (${me.user.email || 'not set'}) is always allowed.`}><Chips values={f.access_emails ?? []} onChange={(v) => set({ access_emails: v })} placeholder="you@example.com" /></Field>
          </div></div>
          <Account me={me} />
        </div>
      </div>
    </>
  );
}

function Account({ me }: { me: Me }) {
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
      <small className="help">Locked out? Run <span className="mono">berth passwd</span> in the container.</small>
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
          <Field label="Default exposure"><select className="input" value={h.exposure || 'lan'} onChange={(e) => seth({ exposure: e.target.value })}>
            {Object.entries(exposureLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
        </div>
        <Toggle on={h.auto_cert} onChange={(v) => seth({ auto_cert: v })} label="Request a certificate for HTTPS sites" help="Halyard issues one through Cloudflare DNS unless an existing certificate (a wildcard, say) already covers the site." />
      </>}
      {connected && !info && <Loading />}
      {!connected && <div className="note" style={{ marginBottom: 0 }}>Save the URL and token, then choose the pool and VIP here.</div>}
      <ErrorBox error={err} />
      <div className="note" style={{ marginBottom: 0, marginTop: 12 }}>Add Halyard's node addresses as trusted proxies above too, so sites see visitors' real addresses.</div>
    </div></div>
  );
}
