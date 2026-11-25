import { Fragment, useState } from 'react';
import { Link } from 'react-router-dom';
import { del, get, post, put } from '../api';
import { useMe } from '../me';
import { Badge, ErrorBox, Field, Icon, Loading, Toggle, useLoad, useToast } from '../ui';

// The Cloudflare plugin's page. Its API lives under /api/p/cloudflare.
const B = '/p/cloudflare';

export interface CFRecord { name: string; status: 'ok' | 'conflict' | 'no_zone' | 'error'; detail?: string; content?: string; proxied: boolean }
export interface CFSite {
  site: string; domain: string; account_id: string; connected: boolean; managed: boolean; on_halyard: boolean; dns_on: boolean;
  proxied: boolean; off: boolean; records: CFRecord[]; checked?: string; note?: string;
}
interface Overview { own_token: boolean; zones: string[] | null; server_token: boolean; dns_on: boolean; proxied: boolean; ipv4: string; ipv6: string; sites: CFSite[] }

const recordBadge = (r: CFRecord) => r.status === 'ok' ? <Badge kind="g">Points here</Badge> : r.status === 'conflict' ? <Badge kind="a">Someone else's record</Badge>
  : r.status === 'no_zone' ? <Badge kind="n">Not in Cloudflare</Badge> : <Badge kind="b">Error</Badge>;

function siteBadge(s: CFSite) {
  if (s.on_halyard) return <Badge kind="n">Halyard manages it</Badge>;
  if (!s.connected) return <Badge kind="n">Not connected</Badge>;
  if (s.off) return <Badge kind="n">Off for this site</Badge>;
  if (!s.managed) return <Badge kind="n">Not managed</Badge>;
  if (s.note) return <Badge kind="b">Needs setup</Badge>;
  if (!s.records.length) return <Badge kind="n">Not checked yet</Badge>;
  return s.records.every((r) => r.status === 'ok') ? <Badge kind="g">Points here</Badge> : <Badge kind="a">Needs attention</Badge>;
}

export default function Cloudflare() {
  const { isAdmin } = useMe();
  const { data, error, reload } = useLoad(() => get<Overview>(B + '/overview'));
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const connected = data.own_token || data.server_token;
  const connect = async () => {
    setErr(null); setBusy(true);
    try { await put(B + '/account', { token }); setToken(''); toast('Cloudflare connected'); reload(); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const sync = () => post<{ sites: number }>(B + '/sync').then((r) => { toast(`Checked ${r.sites} site${r.sites === 1 ? '' : 's'}`); reload(); }).catch((e) => toast(e.message, true));
  return (
    <>
      <div className="head"><div><h1>Cloudflare</h1><div className="sub">Keep your sites' DNS in Cloudflare pointing at this server, get HTTPS certificates through Cloudflare DNS, and clear Cloudflare's cache from a site's page.</div></div>
        {connected && data.dns_on && <button className="btn" onClick={sync}><Icon name="sync" />Check all sites</button>}</div>
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Your Cloudflare account</h3>{connected ? <Badge kind="g">Connected</Badge> : <Badge kind="n">Not connected</Badge>}</div><div className="cb">
            <ErrorBox error={err} />
            {data.server_token && <p className="muted">Your sites use the token set for this plugin on the <Link to="/plugins">Plugins</Link> page. You can use a different one for this account below.</p>}
            {data.own_token && <p className="muted">Connected with your own token{data.zones?.length ? <>, which sees <b>{data.zones.join(', ')}</b></> : ''}.</p>}
            {!connected && <p className="muted">Create an API token in Cloudflare (My Profile → API Tokens → Create Token → Custom token) with these permissions on the zones of your sites, then paste it here:</p>}
            {!connected && <ul className="muted" style={{ margin: '0 0 12px 18px', fontSize: 13 }}><li>Zone → Zone → Read</li><li>Zone → DNS → Edit</li><li>Zone → Cache Purge → Purge</li></ul>}
            <Field label={data.own_token ? 'Replace token' : 'API token'}><input className="input mono" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder="Paste the token" autoComplete="off" /></Field>
            <div className="row" style={{ gap: 8 }}>
              <button className="btn s p" disabled={busy || !token.trim()} onClick={connect}>{data.own_token ? 'Replace' : 'Connect'}</button>
              {data.own_token && <button className="btn s" disabled={busy} onClick={() => confirm('Disconnect Cloudflare? Records already in Cloudflare stay as they are.') && del(B + '/account').then(() => { toast('Disconnected'); reload(); }).catch((e) => toast(e.message, true))}>Disconnect</button>}
            </div>
          </div></div>
        </div>
        <div className="stack">
          <div className="card"><div className="ch"><h3>How it works</h3></div><div className="cb muted" style={{ fontSize: 13.5 }}>
            {data.dns_on ? <>
              <p>Each site's domains get A{data.ipv6 ? ' and AAAA' : ''} records pointing at this server{data.ipv4 && <> (<span className="mono">{data.ipv4}</span>{data.ipv6 && <>, <span className="mono">{data.ipv6}</span></>})</>}, {data.proxied ? 'proxied through Cloudflare' : 'not proxied'} unless you change it per site. When you add or remove a domain, its record follows.</p>
              <p>Records someone else made are never changed until you press Take over on that site.</p>
            </> : <p>{isAdmin ? <>DNS management is off. Turn on "Manage DNS records for sites" in the Cloudflare plugin's settings on the <Link to="/plugins">Plugins</Link> page.</> : 'DNS management is not available yet.'}</p>}
            <p style={{ marginBottom: 0 }}>Certificates for your sites are checked through Cloudflare DNS, so HTTPS works even before the domain points here.</p>
          </div></div>
        </div>
      </div>
      {data.dns_on && <div className="card" style={{ marginTop: 16 }}><div className="ch"><h3>Sites</h3></div>
        {data.sites.length === 0 ? <div className="empty">No sites yet.</div> : <table><thead><tr><th>Site</th><th>DNS</th><th>Records</th></tr></thead><tbody>
          {data.sites.map((s) => <tr key={s.site}>
            <td><Link to={'/sites/' + s.site}>{s.domain}</Link></td>
            <td>{siteBadge(s)}</td>
            <td className="muted" style={{ fontSize: 12.5 }}>{s.note || s.records.map((r) => <div key={r.name}><span className="mono">{r.name}</span>{r.status !== 'ok' && <> · {r.detail}</>}</div>)}</td>
          </tr>)}
        </tbody></table>}
      </div>}
    </>
  );
}

// CloudflareDNSCard is the site page's view of its Cloudflare records.
export function CloudflareDNSCard({ site }: { site: string }) {
  const { data, reload } = useLoad(() => get<CFSite>(B + '/sites/' + site + '/dns'), [site]);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  if (!data || !data.dns_on) return null;
  const set = (body: object, ok: string) => {
    setBusy(true);
    post(B + '/sites/' + site + '/dns', body).then(() => toast(ok)).catch((e) => toast(e.message, true)).finally(() => { setBusy(false); reload(); });
  };
  const conflict = data.records.some((r) => r.status === 'conflict');
  return (
    <div className="card"><div className="ch"><h3>Cloudflare DNS</h3>{siteBadge(data)}</div><div className="cb">
      {data.on_halyard ? <p className="muted">Halyard keeps this site's records while the site is published there.</p>
        : !data.connected ? <p className="muted">Connect your Cloudflare account on the <Link to="/p/cloudflare">Cloudflare</Link> page and this site's records are kept for you.</p>
        : <>
          {data.note && <div className="err">{data.note}</div>}
          {data.managed && data.records.length > 0 && <div className="kv" style={{ marginBottom: 12 }}>{data.records.map((r) => <Fragment key={r.name}>
            <div className="mono" style={{ fontSize: 12.5 }}>{r.name}</div>
            <div>{recordBadge(r)}{r.status === 'ok' ? <span className="dim mono" style={{ fontSize: 12, marginLeft: 6 }}>{r.content}</span> : <div className="dim" style={{ fontSize: 12, marginTop: 4 }}>{r.detail}</div>}</div>
          </Fragment>)}</div>}
          <Toggle on={!data.off} disabled={busy} onChange={(v) => set({ off: !v }, v ? 'DNS is managed for this site' : 'DNS left alone for this site')} label="Manage this site's records" />
          {!data.off && <Toggle on={data.proxied} disabled={busy} onChange={(v) => set({ proxied: v }, v ? 'Proxied through Cloudflare' : 'DNS only')} label="Proxy through Cloudflare (orange cloud)" />}
          <div className="row" style={{ gap: 8 }}>
            {data.managed && <button className="btn s" disabled={busy} onClick={() => set({}, 'Checked')}><Icon name="sync" size={13} />Check now</button>}
            {data.managed && conflict && <button className="btn s p" disabled={busy} onClick={() => confirm('Replace the existing A, AAAA and CNAME records of this site\'s domains with records pointing at this server?') && set({ adopt: true }, 'Records taken over')}>Take over records</button>}
          </div>
        </>}
    </div></div>
  );
}
