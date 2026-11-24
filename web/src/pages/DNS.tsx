import { FormEvent, Fragment, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { AccountView, del, get, post, put } from '../api';
import { useMe } from '../me';
import { Badge, Copyable, ErrorBox, Field, Icon, Loading, Modal, useLoad, useToast } from '../ui';

// The DNS plugin's page. Its API lives under /api/p/dns.
const B = '/p/dns';

interface Info { nameservers: string[]; ipv4: string; ipv6: string; secondary: boolean }
interface ZoneRow { name: string; account_id: string; created?: string; serial: number; unmanaged?: boolean }
export interface DNSSet { name: string; type: string; ttl: number; records: string[]; locked?: boolean }
interface ZoneDetail {
  name: string; account_id: string; serial: number; sets: DNSSet[]; nameservers: string[]; ttl: number;
  delegation: 'ok' | 'other' | 'none' | 'unknown'; found: string[];
}
export interface SiteDNS {
  zone: string; delegation?: string; nameservers: string[]; ipv4: string; ipv6: string;
  hosts: { name: string; found: string[]; points: boolean }[];
}

const types = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV', 'CAA', 'NS'];
const hints: Record<string, string> = {
  A: 'An IPv4 address, like 203.0.113.10',
  AAAA: 'An IPv6 address, like 2001:db8::10',
  CNAME: 'Another host name, like shop.example.org',
  MX: 'Priority and mail server, like 10 mail.example.com',
  TXT: 'Any text, like v=spf1 mx ~all',
  SRV: 'Priority, weight, port and target, like 10 5 5060 sip.example.com',
  CAA: 'Flags, tag and value, like 0 issue letsencrypt.org',
  NS: 'A nameserver, to hand a subdomain to other nameservers',
};

export default function DNS() {
  const { sub } = useParams();
  return sub ? <ZonePage zone={sub} /> : <Zones />;
}

function Zones() {
  const { isAdmin } = useMe();
  const { data, error, reload } = useLoad(() => get<{ zones: ZoneRow[]; error?: string }>(B + '/zones'));
  const info = useLoad(() => get<Info>(B + '/info'));
  const accounts = useLoad(() => (isAdmin ? get<AccountView[]>('/accounts') : Promise.resolve([] as AccountView[])));
  const [adding, setAdding] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const toast = useToast();
  const nav = useNavigate();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const acctName = (id: string) => accounts.data?.find((a) => a.id === id)?.name ?? '';
  const sync = () => {
    setSyncing(true);
    post<{ added: number; removed: number }>(B + '/sync').then((r) => toast(`Secondary in step: ${r.added} added, ${r.removed} removed`))
      .catch((e) => toast(e.message, true)).finally(() => setSyncing(false));
  };
  return (
    <>
      <div className="head">
        <div><h1>DNS</h1><div className="sub">Domains served by {info.data?.nameservers.length ? info.data.nameservers.join(' and ') : 'your nameservers'}.</div></div>
        <div className="row" style={{ gap: 8 }}>
          {isAdmin && info.data?.secondary && <button className="btn" disabled={syncing} onClick={sync} title="Add missing zones to the secondary and remove deleted ones"><Icon name="sync" />Sync secondary</button>}
          <button className="btn p" onClick={() => setAdding(true)}><Icon name="plus" />Add domain</button>
        </div>
      </div>
      {data.error && <div className="err">{data.error}.{isAdmin && <> Check the settings on the <Link to="/plugins">Plugins</Link> page.</>}</div>}
      <div className="card">
        {data.zones.length === 0 ? <div className="empty">
          <p style={{ marginBottom: 14 }}>No domains yet. Add one, then set its nameservers at your registrar.</p>
          <button className="btn p" onClick={() => setAdding(true)}><Icon name="plus" />Add a domain</button>
        </div> : (
          <table>
            <thead><tr><th>Domain</th>{isAdmin && <th>Account</th>}<th>Serial</th></tr></thead>
            <tbody>{data.zones.map((z) => (
              <tr key={z.name} className="click" onClick={() => nav('/p/dns/' + z.name)}>
                <td><b>{z.name}</b></td>
                {isAdmin && <td className="muted">{z.unmanaged ? <Badge kind="n">Not in Berth</Badge> : acctName(z.account_id) || <span className="dim">deleted account</span>}</td>}
                <td className="muted mono">{z.serial || ''}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </div>
      {adding && <AddZone accounts={accounts.data ?? []} onClose={(name) => { setAdding(false); if (name) nav('/p/dns/' + name); else reload(); }} />}
    </>
  );
}

function AddZone({ accounts, onClose }: { accounts: AccountView[]; onClose: (name?: string) => void }) {
  const { me, isAdmin } = useMe();
  const [name, setName] = useState('');
  const [account, setAccount] = useState(me?.user.account_id ?? '');
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setErr(null); setBusy(true);
    try {
      const r = await post<{ name: string; warning: string }>(B + '/zones', { name, account_id: account });
      toast(r.warning || 'Added ' + r.name, !!r.warning);
      onClose(r.name);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <Modal title="Add a domain" onClose={() => onClose()}>
      <form onSubmit={submit}>
        <ErrorBox error={err} />
        <Field label="Domain" help="The domain you registered, like example.com. Records for your sites in it are added for you.">
          <input className="input mono" autoFocus value={name} onChange={(e) => setName(e.target.value.trim().toLowerCase())} placeholder="example.com" />
        </Field>
        {isAdmin && accounts.length > 1 && <Field label="Account">
          <select className="input" value={account} onChange={(e) => setAccount(e.target.value)}>
            {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}{a.id === me?.user.account_id ? ' (you)' : ''}</option>)}</select></Field>}
        <div className="row" style={{ gap: 8, justifyContent: 'flex-end' }}>
          <button type="button" className="btn" onClick={() => onClose()}>Cancel</button>
          <button className="btn p" disabled={busy || !name}>{busy ? 'Adding…' : 'Add domain'}</button>
        </div>
      </form>
    </Modal>
  );
}

function Delegation({ z }: { z: ZoneDetail }) {
  if (z.delegation === 'ok') return null;
  return (
    <div className="note" style={{ marginBottom: 16 }}>
      <b>{z.delegation === 'other' ? `${z.name} still uses other nameservers` : z.delegation === 'none' ? `${z.name} is not using these nameservers yet` : `Could not check ${z.name}'s nameservers`}.</b>{' '}
      At the registrar where you bought the domain, set its nameservers to:
      <div className="row" style={{ gap: 8, marginTop: 8, flexWrap: 'wrap' }}>{z.nameservers.map((n) => <Copyable key={n} value={n} />)}</div>
      {z.delegation === 'other' && <div className="dim" style={{ marginTop: 6, fontSize: 12 }}>Now: {z.found.join(', ')}. Changes at the registrar can take up to a day to show.</div>}
    </div>
  );
}

function ZonePage({ zone }: { zone: string }) {
  const { isAdmin } = useMe();
  const { data, error, reload } = useLoad(() => get<ZoneDetail>(B + '/zones/' + zone), [zone]);
  const accounts = useLoad(() => (isAdmin ? get<AccountView[]>('/accounts') : Promise.resolve([] as AccountView[])));
  const [edit, setEdit] = useState<DNSSet | null>(null);
  const [isNew, setIsNew] = useState(false);
  const toast = useToast();
  const nav = useNavigate();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const remove = (s: DNSSet) => {
    if (!confirm(`Delete the ${s.type} record${s.records.length > 1 ? 's' : ''} for ${s.name}?`)) return;
    post(B + '/zones/' + zone + '/delete-records', { name: s.name, type: s.type }).then(() => { toast('Deleted'); reload(); }).catch((e) => toast(e.message, true));
  };
  const dropZone = () => {
    if (!confirm(`Delete ${zone} and all its records? Websites and mail on it stop working if the domain still uses these nameservers.`)) return;
    del<{ warning: string }>(B + '/zones/' + zone).then((r) => { toast(r.warning || 'Deleted ' + zone, !!r.warning); nav('/p/dns'); }).catch((e) => toast(e.message, true));
  };
  const move = (account: string) => put(B + '/zones/' + zone + '/account', { account_id: account }).then(() => { toast('Moved'); reload(); }).catch((e) => toast(e.message, true));
  return (
    <>
      <div className="head">
        <div><div className="sub"><Link to="/p/dns">DNS</Link> /</div><h1>{zone}</h1></div>
        <div className="row" style={{ gap: 8 }}>
          <button className="btn p" onClick={() => { setIsNew(true); setEdit({ name: '', type: 'A', ttl: data.ttl, records: [''] }); }}><Icon name="plus" />Add record</button>
        </div>
      </div>
      <Delegation z={data} />
      <div className="card"><div className="tscroll"><table>
        <thead><tr><th>Name</th><th>Type</th><th>Value</th><th>TTL</th><th></th></tr></thead>
        <tbody>{data.sets.map((s) => (
          <tr key={s.name + '/' + s.type}>
            <td className="mono">{s.name}</td>
            <td><Badge kind={s.locked ? 'n' : 'v'}>{s.type}</Badge></td>
            <td className="mono" style={{ fontSize: 12.5, wordBreak: 'break-all' }}>{s.records.map((r) => <div key={r}>{r}</div>)}</td>
            <td className="muted">{s.ttl}</td>
            <td style={{ whiteSpace: 'nowrap', textAlign: 'right' }}>{!s.locked && <>
              <button className="btn s ghost" onClick={() => { setIsNew(false); setEdit(s); }}>Edit</button>
              <button className="btn s ghost" onClick={() => remove(s)}>Delete</button></>}</td>
          </tr>
        ))}</tbody>
      </table></div></div>
      <div className="row" style={{ gap: 12, marginTop: 16, alignItems: 'center', flexWrap: 'wrap' }}>
        {isAdmin && (accounts.data?.length ?? 0) > 0 && <label className="row" style={{ gap: 8, alignItems: 'center' }}><span className="muted">Account</span>
          <select className="input" style={{ width: 'auto' }} value={data.account_id} onChange={(e) => move(e.target.value)}>
            {!data.account_id && <option value="">None</option>}
            {accounts.data!.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>}
        <span style={{ flex: 1 }} />
        <button className="btn danger" onClick={dropZone}><Icon name="trash" />Delete domain</button>
      </div>
      {edit && <EditSet zone={zone} set={edit} isNew={isNew} onClose={(saved) => { setEdit(null); if (saved) reload(); }} />}
    </>
  );
}

function EditSet({ zone, set, isNew, onClose }: { zone: string; set: DNSSet; isNew: boolean; onClose: (saved: boolean) => void }) {
  const [f, setF] = useState<DNSSet>({ ...set, records: set.records.length ? [...set.records] : [''] });
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const setRec = (i: number, v: string) => setF({ ...f, records: f.records.map((r, j) => (j === i ? v : r)) });
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setErr(null); setBusy(true);
    try {
      await put(B + '/zones/' + zone + '/records', { ...f, old: isNew ? undefined : { name: set.name, type: set.type } });
      toast('Saved'); onClose(true);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const fullName = !f.name || f.name === '@' ? zone : f.name.endsWith(zone) ? f.name : f.name + '.' + zone;
  return (
    <Modal title={isNew ? 'Add record' : 'Edit record'} onClose={() => onClose(false)}>
      <form onSubmit={submit}>
        <ErrorBox error={err} />
        <div className="grid3">
          <Field label="Name" help={<span className="mono">{fullName}</span>}><input className="input mono" value={f.name} placeholder="@ or www" onChange={(e) => setF({ ...f, name: e.target.value.trim() })} /></Field>
          <Field label="Type"><select className="input" value={f.type} onChange={(e) => setF({ ...f, type: e.target.value })}>{types.map((t) => <option key={t}>{t}</option>)}</select></Field>
          <Field label="TTL (seconds)"><input className="input" type="number" min={60} value={f.ttl} onChange={(e) => setF({ ...f, ttl: +e.target.value })} /></Field>
        </div>
        <Field label={f.type === 'CNAME' ? 'Value' : 'Values'} help={hints[f.type]}>
          <div className="stack" style={{ gap: 6 }}>
            {f.records.map((r, i) => (
              <div key={i} className="row" style={{ gap: 6 }}>
                <input className="input mono" value={r} onChange={(e) => setRec(i, e.target.value)} />
                {f.records.length > 1 && <button type="button" className="btn s ghost" onClick={() => setF({ ...f, records: f.records.filter((_, j) => j !== i) })}><Icon name="x" /></button>}
              </div>
            ))}
            {f.type !== 'CNAME' && <button type="button" className="btn s ghost" style={{ alignSelf: 'flex-start' }} onClick={() => setF({ ...f, records: [...f.records, ''] })}><Icon name="plus" />Another value</button>}
          </div>
        </Field>
        <div className="row" style={{ gap: 8, justifyContent: 'flex-end' }}>
          <button type="button" className="btn" onClick={() => onClose(false)}>Cancel</button>
          <button className="btn p" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
        </div>
      </form>
    </Modal>
  );
}

// SiteDNSCard tells a site's owner whether its domains reach this server.
export function SiteDNSCard({ site }: { site: string }) {
  const { data } = useLoad(() => get<SiteDNS>(B + '/sites/' + site), [site]);
  if (!data) return null;
  const allOk = data.hosts.every((h) => h.points);
  return (
    <div className="card"><div className="ch"><h3>DNS</h3>{allOk ? <Badge kind="g">Points here</Badge> : <Badge kind="a">Not pointed yet</Badge>}</div><div className="cb">
      <div className="dim" style={{ fontSize: 12.5, marginBottom: 8 }}>Where each domain points on the internet right now:</div>
      <div className="kv">{data.hosts.map((h) => <Fragment key={h.name}>
        <div className="mono" style={{ fontSize: 12.5 }}>{h.name}</div>
        <div>{h.points ? <span style={{ color: 'var(--good-ink)' }}>✓ this server</span> : h.found.length ? <span className="muted mono" style={{ fontSize: 12.5 }}>{h.found.join(', ')}</span> : <span className="dim">not found in DNS yet</span>}</div>
      </Fragment>)}</div>
      {!allOk && <div className="muted" style={{ marginTop: 12, fontSize: 13 }}>
        {data.zone ? <>
          {data.delegation === 'ok' ? <>Records are in <Link to={'/p/dns/' + data.zone}>{data.zone}</Link>; changes can take an hour to reach everyone.</>
            : <>Set the nameservers for {data.zone} at your registrar to <b className="mono">{data.nameservers.join(', ')}</b>. <Link to={'/p/dns/' + data.zone}>Open the zone</Link>.</>}
        </> : <>
          Point the domains at this server{data.ipv4 && <> with an A record to <b className="mono">{data.ipv4}</b></>}{data.ipv6 && <> and an AAAA record to <b className="mono">{data.ipv6}</b></>},
          {' '}or <Link to="/p/dns">add the domain to DNS</Link> and use nameservers <b className="mono">{data.nameservers.join(', ')}</b>.
        </>}
      </div>}
    </div></div>
  );
}
