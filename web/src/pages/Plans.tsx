import { useState } from 'react';
import { bytes, del, get, Plan, PlanUsage, post, put } from '../api';
import { Badge, ErrorBox, Field, Icon, Loading, Modal, Toggle, useLoad, useToast } from '../ui';

type PlanView = Plan & { accounts: number };

const blank: Plan = { id: '', name: '', max_sites: 5, disk_mb: 10240, dns_zones: 5, max_domains: 5, memory_mb: 256, workers: 5, upload_mb: 64, db_connections: 20, cpu_percent: 0, ram_mb: 0, max_processes: 0, allow_shell: false, no_sftp: false, mail_domains: 0, mailboxes: 0, mail_aliases: 0, mail_storage_mb: 0, no_sites: false };

export const limit = (n: number, unit = '') => (n ? n + unit : 'Unlimited');
export const mbLabel = (n: number) => (!n ? 'Unlimited' : n >= 1024 && n % 1024 === 0 ? n / 1024 + ' GB' : n + ' MB');

// Plans are hosting packages: what an account may use.
export default function Plans() {
  const { data, error, reload } = useLoad(() => get<PlanView[]>('/plans'));
  const [edit, setEdit] = useState<Plan | null>(null);
  const toast = useToast();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const remove = (p: PlanView) => {
    if (!confirm(`Delete plan ${p.name}?`)) return;
    del('/plans/' + p.id).then(() => { toast('Deleted'); reload(); }).catch((e) => toast(e.message, true));
  };
  return (
    <>
      <div className="head">
        <div><h1>Plans</h1><div className="sub">Hosting packages. Give an account a plan on its page; accounts without one have no limits.</div></div>
        <button className="btn p" onClick={() => setEdit({ ...blank })}><Icon name="plus" />New plan</button>
      </div>
      <div className="card">
        {data.length === 0 ? <div className="empty"><p style={{ marginBottom: 14 }}>No plans yet.</p><button className="btn p" onClick={() => setEdit({ ...blank })}><Icon name="plus" />Create a plan</button></div> : (
          <div className="tscroll"><table>
            <thead><tr><th>Plan</th><th>Sites</th><th>Disk</th><th>Domains<br />per site</th><th>PHP per site</th><th>DNS<br />domains</th><th>CPU · memory</th><th>Mail</th><th></th></tr></thead>
            <tbody>{data.map((p) => (
              <tr key={p.id} className="click" onClick={() => setEdit(p)}>
                <td><b>{p.name}</b><div className="dim" style={{ fontSize: 12.5, display: 'flex', gap: 6, alignItems: 'center', marginTop: 2 }}>{p.accounts} account{p.accounts === 1 ? '' : 's'}{p.allow_shell && <Badge kind="a">shell</Badge>}{p.no_sites && <Badge kind="v">mail only</Badge>}</div></td>
                {p.no_sites ? <td colSpan={6} className="muted wrap">No websites (mail only)</td> : <>
                <td>{limit(p.max_sites)}</td>
                <td>{mbLabel(p.disk_mb)}</td>
                <td>{limit(p.max_domains)}</td>
                <td className="muted">{mbLabel(p.memory_mb)} · {limit(p.workers, ' workers')}</td>
                <td>{limit(p.dns_zones)}</td>
                <td className="muted">{p.cpu_percent || p.ram_mb ? `${p.cpu_percent ? p.cpu_percent + '%' : '–'} · ${p.ram_mb ? mbLabel(p.ram_mb) : '–'}` : '—'}</td>
                </>}
                <td className="muted wrap">{p.mail_domains > 0 ? `${p.mail_domains} domain${p.mail_domains === 1 ? '' : 's'} · ${limit(p.mailboxes, ' mailboxes')} · ${mbLabel(p.mail_storage_mb)}` : '—'}</td>
                <td style={{ textAlign: 'right' }}>{p.accounts === 0 && <button className="btn s ghost" onClick={(e) => { e.stopPropagation(); remove(p); }}>Delete</button>}</td>
              </tr>
            ))}</tbody>
          </table></div>
        )}
      </div>
      {edit && <PlanModal plan={edit} onClose={(saved) => { setEdit(null); if (saved) reload(); }} />}
    </>
  );
}

function PlanModal({ plan, onClose }: { plan: Plan; onClose: (saved: boolean) => void }) {
  const [f, setF] = useState<Plan>(plan);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const set = (p: Partial<Plan>) => setF({ ...f, ...p });
  const num = (k: keyof Plan, label: string, help?: string) => (
    <Field label={label} help={help}><input className="input" type="number" min={0} value={f[k] as number} onChange={(e) => set({ [k]: e.target.value === '' ? 0 : +e.target.value } as Partial<Plan>)} /></Field>
  );
  const save = async () => {
    setErr(null); setBusy(true);
    try {
      await (f.id ? put('/plans/' + f.id, f) : post('/plans', f));
      toast(f.id ? 'Plan saved; its accounts follow it now' : 'Plan created'); onClose(true);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <Modal title={f.id ? 'Edit plan' : 'New plan'} onClose={() => onClose(false)} wide>
      <ErrorBox error={err} />
      <Field label="Name"><input className="input" autoFocus value={f.name} onChange={(e) => set({ name: e.target.value })} placeholder="Starter" /></Field>
      <div className="navlabel" style={{ padding: '6px 0 10px' }}>Per account (0 = unlimited)</div>
      <div className="grid3">
        {num('max_sites', 'Sites')}
        {num('disk_mb', 'Disk (MB)', 'Files and databases of all sites. Over it, no new sites or uploads.')}
        {num('dns_zones', 'DNS domains', 'With the DNS hosting plugin.')}
        {num('cpu_percent', 'CPU (%)', '100 is one core, for all of the account\'s PHP together.')}
        {num('ram_mb', 'Memory (MB)', 'For all of the account\'s PHP together.')}
        {num('max_processes', 'Processes')}
      </div>
      {(f.cpu_percent > 0 || f.ram_mb > 0 || f.max_processes > 0) && <small className="help" style={{ display: 'block', marginTop: -6, marginBottom: 12 }}>With any of these, the account's sites run in PHP-FPM processes of their own, in a systemd slice that holds the limits.</small>}
      <div className="navlabel" style={{ padding: '6px 0 10px' }}>Per site (0 = server default or unlimited)</div>
      <div className="grid3">
        {num('max_domains', 'Domains', 'The main domain plus other domains.')}
        {num('memory_mb', 'PHP memory (MB)')}
        {num('workers', 'PHP workers', 'Requests a site runs at once.')}
        {num('upload_mb', 'Upload size (MB)')}
        {num('db_connections', 'Database connections')}
      </div>
      <div className="navlabel" style={{ padding: '6px 0 10px' }}>Mail, with the Mailcow plugin (mail domains 0 = no mail)</div>
      <div className="grid3">
        {num('mail_domains', 'Mail domains', 'Domains the account can add on the Mail page.')}
        {num('mailboxes', 'Mailboxes', 'Across all its mail domains. 0 = unlimited.')}
        {num('mail_aliases', 'Aliases', 'Across all its mail domains. 0 = unlimited.')}
        {num('mail_storage_mb', 'Mail storage (MB)', 'All mailboxes together. 0 = the plugin\'s default per domain.')}
      </div>
      <Toggle on={f.allow_shell} onChange={(v) => set({ allow_shell: v })} label="Allow PHP shell functions" help="exec(), system(), proc_open() and the like. Off is safer on shared hosting; a few plugins need them." />
      <Toggle on={!f.no_sftp} onChange={(v) => set({ no_sftp: !v })} label="SFTP access" />
      <Toggle on={f.no_sites} onChange={(v) => set({ no_sites: v })} label="Mail only" help="The account cannot create websites; for email-only plans." />
      <div className="row" style={{ justifyContent: 'flex-end', gap: 8 }}>
        <button className="btn" onClick={() => onClose(false)}>Cancel</button>
        <button className="btn p" disabled={busy} onClick={save}>{f.id ? 'Save plan' : 'Create plan'}</button>
      </div>
    </Modal>
  );
}

// UsageCard shows what an account uses of its plan.
export function UsageCard({ u, title = 'Plan' }: { u: PlanUsage; title?: string }) {
  const p = u.plan;
  const bar = (used: number, max: number) => {
    const pct = max ? Math.min(100, Math.round((used / max) * 100)) : 0;
    return max ? <div className="meter" style={{ height: 6, borderRadius: 3, background: 'var(--line)', marginTop: 6 }}>
      <div style={{ width: pct + '%', height: '100%', borderRadius: 3, background: pct >= 100 ? 'var(--bad)' : pct >= 85 ? 'var(--warn)' : 'var(--good)' }} /></div> : null;
  };
  return (
    <div className="card"><div className="ch"><h3>{title}</h3>{p ? <Badge kind="v">{p.name}</Badge> : <Badge kind="n">No limits</Badge>}</div><div className="cb">
      <div className="kv">
        <div>Sites</div><div>{u.sites}{p?.max_sites ? ' of ' + p.max_sites : ''}{p && bar(u.sites, p.max_sites)}</div>
        <div>Disk</div><div>{bytes(u.disk_used)}{p?.disk_mb ? ' of ' + mbLabel(p.disk_mb) : ''}{p && bar(u.disk_used, p.disk_mb * 1048576)}</div>
        {p && <>
          <div>Per site</div><div className="muted">{limit(p.max_domains, ' domains')} · {mbLabel(p.memory_mb)} PHP memory · {limit(p.workers, ' workers')} · {mbLabel(p.upload_mb)} uploads</div>
          {p.dns_zones > 0 && <><div>DNS domains</div><div>{p.dns_zones}</div></>}
          {(p.cpu_percent > 0 || p.ram_mb > 0) && <><div>Resources</div><div className="muted">{p.cpu_percent ? p.cpu_percent + '% CPU' : 'any CPU'} · {p.ram_mb ? mbLabel(p.ram_mb) + ' memory' : 'any memory'} for all sites</div></>}
        </>}
      </div>
    </div></div>
  );
}
