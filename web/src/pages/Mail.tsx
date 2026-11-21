import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { bytes, del, get, post, put } from '../api';
import { Badge, Copyable, ErrorBox, Field, Icon, Loading, Modal, Toggle, useLoad, useToast } from '../ui';

// The mailcow plugin's page. Its API lives under /api/p/mailcow.
const B = '/p/mailcow';

interface Domain {
  domain: string; description: string; active: boolean; mailboxes: number; max_mailboxes: number; aliases: number; max_aliases: number;
  quota_mb: number; def_quota_mb: number; max_quota_mb: number; bytes_used: number; relayhost_id: string; messages: number;
  ses_transport: boolean; transport_host: string; site?: string; pending?: boolean;
}
interface Overview {
  domains: Domain[]; suggestions: string[]; ses: boolean; ses_default: boolean; dns_mode: string; mail_host: string; url: string;
  mailbox_quota_mb: number; error?: string;
}
interface Mailbox { address: string; local_part: string; name: string; quota_mb: number; bytes_used: number; messages: number; active: boolean; last_login?: number }
interface Alias { id: string; address: string; goto: string; active: boolean }
interface DNSRecord { type: string; name: string; content: string; priority?: number; purpose: string; status: string; existing?: string[]; error?: string }
interface Step { name: string; status: string; detail?: string }
interface Setup { steps: Step[]; records: DNSRecord[] }
interface Detail {
  domain: Domain; mailboxes: Mailbox[]; aliases: Alias[]; records: DNSRecord[]; dns_mode: string; mailbox_error?: string; alias_error?: string;
  ses: { configured: boolean; identity?: boolean; verified?: boolean; dkim_status?: string; mail_from?: string; mail_from_status?: string; error?: string };
}

export default function Mail() {
  const { sub } = useParams();
  return sub ? <DomainPage domain={sub} /> : <Domains />;
}

function Sending({ d }: { d: Domain }) {
  if (d.ses_transport) return <Badge kind="g">Amazon SES</Badge>;
  if (d.pending) return <Badge kind="a">Waiting for SES</Badge>;
  if (d.transport_host) return <Badge kind="v">{d.transport_host}</Badge>;
  return <Badge kind="n">Direct</Badge>;
}

function Domains() {
  const { data, error, reload } = useLoad(() => get<Overview>(B + '/overview'), [], 30000);
  const [adding, setAdding] = useState(false);
  const nav = useNavigate();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  return (
    <>
      <div className="head">
        <div><h1>Mail</h1><div className="sub">Mail domains and mailboxes on mailcow{data.mail_host ? <> (<span className="mono">{data.mail_host}</span>)</> : ''}.{data.ses ? ' Outgoing mail goes through Amazon SES.' : ''}</div></div>
        <div className="row" style={{ gap: 8 }}>
          {data.url && <a className="btn" href={data.url} target="_blank" rel="noreferrer"><Icon name="ext" />mailcow</a>}
          <button className="btn p" onClick={() => setAdding(true)} disabled={!!data.error}><Icon name="plus" />Add domain</button>
        </div>
      </div>
      {data.error && <div className="err">{data.error}. Check the settings on the <Link to="/plugins">Plugins</Link> page.</div>}
      <div className="card">
        {data.domains.length === 0 ? <div className="empty">
          <p style={{ marginBottom: 14 }}>No mail domains yet.</p>
          {!data.error && <button className="btn p" onClick={() => setAdding(true)}><Icon name="plus" />Add a domain</button>}
        </div> : (
          <table>
            <thead><tr><th>Domain</th><th>Mailboxes</th><th>Aliases</th><th>Storage</th><th>Sending</th><th>Status</th></tr></thead>
            <tbody>{data.domains.map((d) => (
              <tr key={d.domain} className="click" onClick={() => nav('/p/mailcow/' + d.domain)}>
                <td><b>{d.domain}</b>{d.description && <div className="dim" style={{ fontSize: 11.5 }}>{d.description}</div>}</td>
                <td className="muted">{d.mailboxes} / {d.max_mailboxes}</td>
                <td className="muted">{d.aliases} / {d.max_aliases}</td>
                <td className="muted">{bytes(d.bytes_used)} of {bytes(d.quota_mb * 1048576)}</td>
                <td><Sending d={d} /></td>
                <td>{d.active ? <Badge kind="g">● Active</Badge> : <Badge kind="w">Disabled</Badge>}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </div>
      {adding && <AddDomain o={data} onClose={() => { setAdding(false); reload(); }} />}
    </>
  );
}

function Steps({ steps }: { steps: Step[] }) {
  const kind: Record<string, string> = { ok: 'g', warn: 'w', error: 'b', skip: 'n' };
  const label: Record<string, string> = { ok: 'Done', warn: 'Note', error: 'Failed', skip: 'Skipped' };
  return (
    <div className="steps">
      {steps.map((s, i) => <div key={i} className="st"><Badge kind={kind[s.status] ?? 'n'}>{label[s.status] ?? s.status}</Badge><div><b>{s.name}</b>{s.detail && <div className="muted">{s.detail}</div>}</div></div>)}
    </div>
  );
}

function AddDomain({ o, onClose }: { o: Overview; onClose: () => void }) {
  const [f, setF] = useState({ domain: '', description: '', ses: o.ses && o.ses_default });
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<Setup | null>(null);
  const nav = useNavigate();
  const go = async () => {
    setErr(null); setBusy(true);
    try { setRes(await post<Setup>(B + '/domains', f)); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  if (res) {
    return (
      <Modal title={'Added ' + f.domain} onClose={onClose} wide>
        <Steps steps={res.steps} />
        <Records records={res.records} />
        <div className="row" style={{ gap: 8, marginTop: 14 }}>
          <button className="btn p" onClick={() => nav('/p/mailcow/' + f.domain.trim().toLowerCase())}>Open domain</button>
          <button className="btn" onClick={onClose}>Close</button>
        </div>
      </Modal>
    );
  }
  return (
    <Modal title="Add mail domain" onClose={onClose}>
      <ErrorBox error={err} />
      <Field label="Domain" help={o.suggestions.length ? 'Suggestions are your Berth sites without mail yet.' : undefined}>
        <input className="input mono" list="mail-suggest" autoFocus value={f.domain} onChange={(e) => setF({ ...f, domain: e.target.value })} placeholder="example.com"
          onKeyDown={(e) => e.key === 'Enter' && go()} />
        <datalist id="mail-suggest">{o.suggestions.map((s) => <option key={s} value={s} />)}</datalist>
      </Field>
      <Field label="Description (optional)"><input className="input" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></Field>
      {o.ses
        ? <Toggle on={f.ses} onChange={(v) => setF({ ...f, ses: v })} label="Send through Amazon SES"
            help="Adds the domain to SES with DKIM and a custom MAIL FROM, and switches on the SES transport in mailcow as soon as SES has verified it." />
        : <p className="muted" style={{ marginBottom: 14 }}>Amazon SES is not set up, so mail goes out directly from mailcow. Add SES keys on the Plugins page to change that.</p>}
      <p className="dim" style={{ fontSize: 12, marginBottom: 14 }}>
        {o.dns_mode === 'cloudflare' ? 'Missing DNS records are created in Cloudflare; existing ones are never changed.' : 'Berth will list the DNS records to add at your DNS provider.'}
      </p>
      <button className="btn p" disabled={busy || !f.domain.trim()} onClick={go}>{busy ? 'Setting up…' : 'Add domain'}</button>
    </Modal>
  );
}

function RecordStatus({ r }: { r: DNSRecord }) {
  if (r.status === 'ok') return <Badge kind="g">OK</Badge>;
  if (r.status === 'created') return <Badge kind="g">Created</Badge>;
  if (r.status === 'missing') return <Badge kind="w">Missing</Badge>;
  if (r.status === 'conflict') return <span title={'Already there: ' + (r.existing ?? []).join(' | ')}><Badge kind="o">Clash</Badge></span>;
  return <span title={r.error}><Badge kind="b">Error</Badge></span>;
}

function Records({ records }: { records: DNSRecord[] }) {
  const [show, setShow] = useState<DNSRecord | null>(null);
  return (
    <>
      <div className="tscroll"><table>
        <thead><tr><th>Type</th><th>Name</th><th>Value</th><th>Status</th></tr></thead>
        <tbody>{records.map((r, i) => (
          <tr key={i} className="click" onClick={() => setShow(r)}>
            <td className="mono">{r.type}</td>
            <td className="mono">{r.name}<div className="dim" style={{ fontSize: 11, fontFamily: 'inherit' }}>{r.purpose}</div></td>
            <td className="mono recval">{r.priority ? r.priority + ' ' : ''}{r.content.length > 70 ? r.content.slice(0, 60) + '…' : r.content}</td>
            <td><RecordStatus r={r} /></td>
          </tr>
        ))}</tbody>
      </table></div>
      {show && <Modal title={show.type + ' ' + show.name} onClose={() => setShow(null)} wide>
        <p className="muted" style={{ marginBottom: 12 }}>{show.purpose}</p>
        <Field label="Name"><Copyable value={show.name} /></Field>
        {show.priority ? <Field label="Priority"><Copyable value={String(show.priority)} /></Field> : null}
        <Field label="Value"><Copyable value={show.content} /></Field>
        {show.status === 'conflict' && <div className="err">This name already has: {(show.existing ?? []).join('  |  ')}. Berth does not change existing records{show.type === 'TXT' && show.content.startsWith('v=spf1') ? '; merge the SPF includes by hand (one SPF record per name)' : ''}.</div>}
        {show.error && <div className="err">{show.error}</div>}
        {show.type !== 'TXT' && show.type !== 'MX' && <p className="dim" style={{ fontSize: 12 }}>In Cloudflare keep this record DNS only (grey cloud).</p>}
      </Modal>}
    </>
  );
}

function DomainPage({ domain }: { domain: string }) {
  const { data, error, reload } = useLoad(() => get<Detail>(B + '/domains/' + domain), [domain]);
  const [setup, setSetup] = useState<Setup | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [mbox, setMbox] = useState<Mailbox | 'new' | null>(null);
  const [newPw, setNewPw] = useState<{ address: string; password: string } | null>(null);
  const toast = useToast();
  const nav = useNavigate();
  if (!data) return error ? <><Link to="/p/mailcow" className="btn s ghost" style={{ marginBottom: 12 }}>← Mail</Link><ErrorBox error={error} /></> : <Loading />;
  const d = data.domain;
  const act = async (fn: () => Promise<unknown>, ok?: string) => {
    setErr(null); setBusy(true);
    try { const r = await fn(); if (ok) toast(ok); reload(); return r; } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const fix = () => act(async () => setSetup(await post<Setup>(B + '/domains/' + domain + '/setup')));
  const transport = (on: boolean) => act(async () => toast((await put<{ message: string }>(B + '/domains/' + domain, { ses_transport: on })).message));
  const remove = () => {
    if (!confirm(`Delete the mail domain ${domain} from mailcow${data.ses.identity ? ' and Amazon SES' : ''}? DNS records are left as they are.`)) return;
    act(async () => { await del(B + '/domains/' + domain + '?ses=1'); nav('/p/mailcow'); }, 'Domain deleted');
  };
  const missing = data.records.filter((r) => r.status !== 'ok').length;
  const s = data.ses;
  return (
    <>
      <Link to="/p/mailcow" className="btn s ghost" style={{ marginBottom: 12 }}>← Mail</Link>
      <div className="head">
        <div><h1>{d.domain}</h1><div className="sub">{d.description || 'Mail domain'}{d.site && <> · <Link to={'/sites/' + d.site}>Berth site</Link></>}</div></div>
        <div className="row" style={{ gap: 8 }}>
          <button className="btn" disabled={busy} onClick={() => act(() => put(B + '/domains/' + domain, { active: !d.active }), d.active ? 'Domain disabled' : 'Domain enabled')}>{d.active ? 'Disable' : 'Enable'}</button>
          <button className="btn danger" disabled={busy} onClick={remove}><Icon name="trash" />Delete</button>
        </div>
      </div>
      <ErrorBox error={err} />
      {setup && <div className="card" style={{ marginBottom: 16 }}><div className="ch"><h3>Check and fix</h3><button className="btn s ghost" onClick={() => setSetup(null)}><Icon name="x" size={13} /></button></div><div className="cb"><Steps steps={setup.steps} /></div></div>}
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="stack">
          <div className="card">
            <div className="ch"><h3>Mailboxes</h3><button className="btn s p" onClick={() => setMbox('new')}><Icon name="plus" size={13} />Add mailbox</button></div>
            {data.mailbox_error && <div className="cb"><div className="err">{data.mailbox_error}</div></div>}
            {data.mailboxes.length === 0 ? <div className="empty">No mailboxes yet.</div> : (
              <table>
                <thead><tr><th>Address</th><th>Storage</th><th>Status</th></tr></thead>
                <tbody>{data.mailboxes.map((m) => (
                  <tr key={m.address} className="click" onClick={() => setMbox(m)}>
                    <td><b>{m.address}</b>{m.name && <div className="dim" style={{ fontSize: 11.5 }}>{m.name}</div>}</td>
                    <td className="muted">{bytes(m.bytes_used) === '—' ? '0' : bytes(m.bytes_used)} of {m.quota_mb ? bytes(m.quota_mb * 1048576) : 'unlimited'}</td>
                    <td>{m.active ? <Badge kind="g">● Active</Badge> : <Badge kind="w">Disabled</Badge>}</td>
                  </tr>
                ))}</tbody>
              </table>
            )}
          </div>
          <Aliases domain={domain} aliases={data.aliases} error={data.alias_error} reload={reload} />
        </div>
        <div className="stack">
          <div className="card">
            <div className="ch"><h3>Outgoing mail</h3><Sending d={d} /></div>
            <div className="cb">
              {!s.configured ? <p className="muted">Amazon SES is not set up; mail goes out directly from mailcow. Add SES keys on the <Link to="/plugins">Plugins</Link> page.</p> : <>
                {s.error && <div className="err">{s.error}</div>}
                <div className="kv" style={{ marginBottom: 14 }}>
                  <div>SES identity</div><div>{s.identity ? (s.verified ? <Badge kind="g">Verified</Badge> : <Badge kind="a">Verifying</Badge>) : <Badge kind="n">Not in SES</Badge>}</div>
                  {s.identity && <><div>DKIM</div><div className="mono">{(s.dkim_status ?? '').toLowerCase()}</div></>}
                  {s.mail_from && <><div>MAIL FROM</div><div className="mono">{s.mail_from} <span className="dim">({(s.mail_from_status ?? '').toLowerCase()})</span></div></>}
                  {d.transport_host && <><div>Transport</div><div className="mono">{d.transport_host}</div></>}
                </div>
                <Toggle on={d.ses_transport || !!d.pending} onChange={transport} label="Send this domain's mail through Amazon SES"
                  help={d.pending ? 'Waiting for SES to verify the domain; Berth checks every 5 minutes and switches the transport on by itself.' : 'Uses mailcow\'s sender-dependent transport for this domain.'} />
              </>}
            </div>
          </div>
        </div>
      </div>
      <div style={{ marginTop: 16 }}>
    <div className="card">
      <div className="ch"><h3>DNS records</h3>
        <div className="row" style={{ gap: 8, alignItems: 'center' }}>
          {missing ? <Badge kind="w">{missing} to fix</Badge> : <Badge kind="g">All good</Badge>}
          <button className="btn s" disabled={busy} onClick={fix} title={data.dns_mode === 'cloudflare' ? 'Create missing records in Cloudflare and re-check SES' : 'Re-check'}>{busy ? 'Checking…' : data.dns_mode === 'cloudflare' ? 'Check and fix' : 'Re-check'}</button>
        </div>
      </div>
      <Records records={data.records} />
      <div className="cb dim" style={{ fontSize: 12 }}>{data.dns_mode === 'cloudflare' ? 'Read from Cloudflare.' : 'Looked up in public DNS. Click a record to copy it.'}</div>
    </div>
      </div>
      {mbox && <MailboxModal domain={domain} m={mbox === 'new' ? null : mbox} onClose={() => setMbox(null)} onDone={(pw) => { setMbox(null); if (pw) setNewPw(pw); reload(); }} />}
      {newPw && <Modal title="Mailbox password" onClose={() => setNewPw(null)}>
        <p className="muted" style={{ marginBottom: 12 }}>Shown once. Sign in at the mailcow webmail or any mail app as <b>{newPw.address}</b>.</p>
        <Field label="Password"><Copyable value={newPw.password} /></Field>
        <button className="btn p" onClick={() => setNewPw(null)}>Done</button>
      </Modal>}
    </>
  );
}

function MailboxModal({ domain, m, onClose, onDone }: { domain: string; m: Mailbox | null; onClose: () => void; onDone: (pw?: { address: string; password: string }) => void }) {
  const [f, setF] = useState({ local_part: m?.local_part ?? '', name: m?.name ?? '', password: '', quota_mb: m?.quota_mb ?? 0, active: m?.active ?? true });
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const go = async (fn: () => Promise<{ address: string; password?: string }>) => {
    setErr(null); setBusy(true);
    try { const r = await fn(); onDone(r.password ? { address: r.address, password: r.password } : undefined); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  if (!m) {
    return (
      <Modal title="Add mailbox" onClose={onClose}>
        <ErrorBox error={err} />
        <Field label="Address"><div className="row" style={{ gap: 6, alignItems: 'center' }}><input className="input mono" autoFocus value={f.local_part} onChange={(e) => setF({ ...f, local_part: e.target.value })} placeholder="hello" /><span className="mono muted">@{domain}</span></div></Field>
        <Field label="Name"><input className="input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <div className="grid2">
          <Field label="Password" help="Blank makes a strong one and shows it once."><input className="input mono" type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
          <Field label="Storage (MB)" help="Blank uses the plugin default."><input className="input" type="number" min={0} value={f.quota_mb || ''} onChange={(e) => setF({ ...f, quota_mb: +e.target.value })} /></Field>
        </div>
        <button className="btn p" disabled={busy || !f.local_part.trim()} onClick={() => go(() => post(B + '/domains/' + domain + '/mailboxes', f))}>Add mailbox</button>
      </Modal>
    );
  }
  const path = B + '/mailboxes/' + encodeURIComponent(m.address);
  return (
    <Modal title={m.address} onClose={onClose}>
      <ErrorBox error={err} />
      <Field label="Name"><input className="input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
      <Field label="Storage (MB)" help="0 means no limit (within the domain's storage)."><input className="input" type="number" min={0} value={f.quota_mb} onChange={(e) => setF({ ...f, quota_mb: +e.target.value })} /></Field>
      <Toggle on={f.active} onChange={(v) => setF({ ...f, active: v })} label="Active" help="A disabled mailbox cannot sign in or receive mail." />
      <div className="row" style={{ gap: 8, marginBottom: 18 }}>
        <button className="btn p" disabled={busy} onClick={() => go(() => put(path, { name: f.name, quota_mb: f.quota_mb, active: f.active }))}>Save</button>
      </div>
      <div style={{ borderTop: '1px solid var(--line)', margin: '0 -18px 16px' }} />
      <Field label="New password" help="Blank makes a strong one and shows it once."><input className="input mono" type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
      <div className="row" style={{ gap: 8 }}>
        <button className="btn" disabled={busy} onClick={() => go(() => put(path, { password: f.password }))}><Icon name="key" />Set password</button>
        <button className="btn danger" disabled={busy} style={{ marginLeft: 'auto' }}
          onClick={() => confirm(`Delete ${m.address} and all of its mail? This cannot be undone.`) && go(async () => { await del(path); return { address: m.address }; })}><Icon name="trash" />Delete</button>
      </div>
    </Modal>
  );
}

function Aliases({ domain, aliases, error, reload }: { domain: string; aliases: Alias[]; error?: string; reload: () => void }) {
  const [f, setF] = useState({ local_part: '', goto: '' });
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  const add = async () => {
    setErr(null);
    try { await post(B + '/domains/' + domain + '/aliases', f); setF({ local_part: '', goto: '' }); toast('Alias added'); reload(); } catch (e) { setErr(e); }
  };
  const remove = async (a: Alias) => {
    if (!confirm(`Delete the alias ${a.address}?`)) return;
    try { await del(B + '/domains/' + domain + '/aliases/' + a.id); toast('Alias deleted'); reload(); } catch (e) { setErr(e); }
  };
  return (
    <div className="card">
      <div className="ch"><h3>Aliases</h3></div>
      {error && <div className="cb"><div className="err">{error}</div></div>}
      {aliases.length > 0 && <table>
        <thead><tr><th>Address</th><th>Delivers to</th><th /></tr></thead>
        <tbody>{aliases.map((a) => (
          <tr key={a.id}><td className="mono">{a.address.startsWith('@') ? <>*{a.address} <span className="dim">(catch-all)</span></> : a.address}</td>
            <td className="mono muted" style={{ wordBreak: 'break-all' }}>{a.goto.split(',').join(', ')}</td>
            <td style={{ textAlign: 'right' }}><button className="btn s ghost" onClick={() => remove(a)} title="Delete"><Icon name="trash" size={13} /></button></td></tr>
        ))}</tbody>
      </table>}
      <div className="cb">
        <ErrorBox error={err} />
        <div className="row" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <input className="input mono" style={{ width: 140 }} value={f.local_part} onChange={(e) => setF({ ...f, local_part: e.target.value })} placeholder="sales or *" />
          <span className="mono muted">@{domain} →</span>
          <input className="input mono" style={{ flex: 1, minWidth: 180 }} value={f.goto} onChange={(e) => setF({ ...f, goto: e.target.value })} placeholder="you@example.com, other@example.com"
            onKeyDown={(e) => e.key === 'Enter' && add()} />
          <button className="btn" disabled={!f.goto.trim()} onClick={add}><Icon name="plus" />Add</button>
        </div>
      </div>
    </div>
  );
}
