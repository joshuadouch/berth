import { ComponentType, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { AccountView, get, PluginField, pluginsChanged, PluginView, post, put } from '../api';
import { Badge, ErrorBox, Field, Icon, Loading, Toggle, useLoad, useToast } from '../ui';
import Mail from './Mail';
import { HalyardPage } from './Settings';

// Plugins with a page of their own, by plugin ID. The page is at /p/<id>.
export const pluginPages: Record<string, ComponentType> = { mailcow: Mail, halyard: HalyardPage };

export default function Plugins() {
  const { data, error, reload } = useLoad(() => get<PluginView[]>('/plugins'));
  const accounts = useLoad(() => get<AccountView[]>('/accounts'));
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  return (
    <>
      <div className="head"><div><h1>Plugins</h1><div className="sub">Extra features built into Berth. Turn one on, fill in its settings and, for per-account plugins, choose the accounts it is on for.</div></div></div>
      <div className="stack">
        {data.map((p) => <PluginCard key={p.id} p={p} accounts={accounts.data ?? []} onSaved={reload} />)}
        {data.length === 0 && <div className="card"><div className="empty">No plugins are built into this version.</div></div>}
      </div>
    </>
  );
}

function PluginCard({ p, accounts, onSaved }: { p: PluginView; accounts: AccountView[]; onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  const [cfg, setCfg] = useState<Record<string, any>>(p.config);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState('');
  const toast = useToast();
  useEffect(() => setCfg(p.config), [p]);

  const save = async (enabled: boolean) => {
    setErr(null); setBusy(true); setResult('');
    try {
      await put('/plugins/' + p.id, { enabled, config: cfg });
      toast(enabled === p.enabled ? 'Saved' : enabled ? `${p.name} is on` : `${p.name} is off`);
      onSaved(); pluginsChanged();
      // Secrets are not sent back: clear what was typed.
      setCfg((c) => { const n = { ...c }; p.fields.filter((f) => f.type === 'secret').forEach((f) => delete n[f.key]); return n; });
    } catch (e) { setErr(e); if (enabled && !p.enabled) setOpen(true); } finally { setBusy(false); }
  };
  const test = async () => {
    setErr(null); setBusy(true); setResult('');
    try { setResult((await post<{ message: string }>('/plugins/' + p.id + '/test', { config: cfg })).message); } catch (e) { setErr(e); } finally { setBusy(false); }
  };

  const sections: [string, PluginField[]][] = [];
  for (const f of p.fields) {
    const s = f.section || 'Settings';
    const hit = sections.find((x) => x[0] === s);
    if (hit) hit[1].push(f); else sections.push([s, [f]]);
  }
  return (
    <div className="card">
      <div className="ch">
        <div className="row" style={{ alignItems: 'center', gap: 10 }}>
          <Icon name={p.icon || 'plug'} />
          <h3>{p.name}</h3>
          {p.enabled ? <Badge kind="g">On</Badge> : <Badge kind="n">Off</Badge>}
          <span className="dim" style={{ fontSize: 11.5 }}>v{p.version}</span>
        </div>
        <div className="row" style={{ gap: 8, alignItems: 'center' }}>
          {p.enabled && p.nav_label && pluginPages[p.id] && <Link className="btn s" to={'/p/' + p.id}>Open {p.nav_label}</Link>}
          <button className="btn s ghost" onClick={() => setOpen(!open)}>{open ? 'Hide settings' : 'Settings'}</button>
          <Toggle on={p.enabled} onChange={(v) => save(v)} />
        </div>
      </div>
      <div className="cb">
        <p className="muted">{p.description}</p>
        {p.per_account && p.enabled && <PluginAccounts p={p} accounts={accounts} onSaved={onSaved} />}
        {open && <div style={{ marginTop: 16 }}>
          <ErrorBox error={err} />
          {sections.map(([name, fields]) => (
            <div key={name} style={{ marginBottom: 8 }}>
              <div className="navlabel" style={{ padding: '6px 0 10px' }}>{name}</div>
              <div className="grid2">
                {fields.map((f) => <PluginInput key={f.key} f={f} value={cfg[f.key]} set={p.secrets_set[f.key]} onChange={(v) => setCfg({ ...cfg, [f.key]: v })} />)}
              </div>
            </div>
          ))}
          {result && <div className="okbox"><Icon name="check" size={13} /> {result}</div>}
          <div className="row" style={{ gap: 8 }}>
            <button className="btn p" disabled={busy} onClick={() => save(p.enabled)}>Save</button>
            {!p.enabled && <button className="btn" disabled={busy} onClick={() => save(true)}>Save and turn on</button>}
            {p.can_test && <button className="btn" disabled={busy} onClick={test}>{busy ? 'Checking…' : 'Test'}</button>}
          </div>
        </div>}
        {!open && err != null && <div style={{ marginTop: 12 }}><ErrorBox error={err} /></div>}
      </div>
    </div>
  );
}

// PluginAccounts chooses the accounts a per-account plugin is on for.
function PluginAccounts({ p, accounts, onSaved }: { p: PluginView; accounts: AccountView[]; onSaved: () => void }) {
  const toast = useToast();
  const [err, setErr] = useState<unknown>(null);
  const on = new Set(p.accounts);
  const toggle = (id: string, v: boolean) => {
    const next = accounts.map((a) => a.id).filter((x) => (x === id ? v : on.has(x)));
    setErr(null);
    put('/plugins/' + p.id + '/accounts', { accounts: next }).then(() => { toast('Saved'); onSaved(); pluginsChanged(); }).catch(setErr);
  };
  const eligible = (a: AccountView) => !p.admin_only || a.users.some((u) => u.role === 'admin');
  const shown = accounts.filter((a) => eligible(a) || on.has(a.id));
  return (
    <div style={{ marginTop: 14 }}>
      <div className="navlabel" style={{ padding: '0 0 8px' }}>On for{p.admin_only ? ' (your own accounts only)' : ''}</div>
      <ErrorBox error={err} />
      <div className="row" style={{ gap: 18, flexWrap: 'wrap' }}>
        {shown.map((a) => <Toggle key={a.id} on={on.has(a.id)} onChange={(v) => toggle(a.id, v)} label={a.name} />)}
        {shown.length === 0 && <span className="dim">No accounts can use it.</span>}
      </div>
      {p.accounts.length === 0 && <small className="help">It is on for nobody yet, so it does nothing.</small>}
    </div>
  );
}

function PluginInput({ f, value, set, onChange }: { f: PluginField; value: any; set?: boolean; onChange: (v: any) => void }) {
  const label = f.label + (f.required ? ' *' : '');
  if (f.type === 'bool') {
    return <div style={{ gridColumn: '1 / -1' }}><Toggle on={!!value} onChange={onChange} label={f.label} help={f.help} /></div>;
  }
  if (f.type === 'secret') {
    return (
      <Field label={label} help={f.help}>
        <input className="input mono" type="password" autoComplete="new-password" value={value ?? ''} onChange={(e) => onChange(e.target.value)}
          placeholder={set ? 'Saved; type to replace, or - to clear' : f.placeholder || 'Not set'} />
      </Field>
    );
  }
  if (f.type === 'select') {
    return <Field label={label} help={f.help}><select className="input" value={value ?? ''} onChange={(e) => onChange(e.target.value)}>
      {f.options?.map((o) => <option key={o.value} value={o.value}>{o.label || o.value}</option>)}</select></Field>;
  }
  if (f.type === 'textarea') {
    return <Field label={label} help={f.help} style={{ gridColumn: '1 / -1' }}><textarea className="input mono" rows={4} value={value ?? ''} placeholder={f.placeholder} onChange={(e) => onChange(e.target.value)} /></Field>;
  }
  return (
    <Field label={label} help={f.help}>
      <input className={'input' + (f.type === 'number' ? '' : ' mono')} type={f.type === 'number' ? 'number' : 'text'} value={value ?? ''} placeholder={f.placeholder}
        onChange={(e) => onChange(f.type === 'number' ? (e.target.value === '' ? '' : +e.target.value) : e.target.value)} />
    </Field>
  );
}

// PluginPage shows a plugin's own page at /p/<id>.
export function PluginPage({ id }: { id: string }) {
  const Page = pluginPages[id];
  if (!Page) return <div className="empty">This plugin has no page.</div>;
  return <Page />;
}
