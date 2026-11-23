import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { AccountView, ago, del, get, Plan, post, put, SiteRow, User } from '../api';
import { UsageCard } from './Plans';
import { useMe } from '../me';
import { Badge, ErrorBox, Field, Icon, Loading, Modal, SiteAvatar, StatusBadge, Toggle, useLoad, useToast } from '../ui';

// Accounts lists hosting customers (and the admin's own account).
export default function Accounts() {
  const { data, error, reload } = useLoad(() => get<AccountView[]>('/accounts'));
  const [adding, setAdding] = useState(false);
  const [q, setQ] = useState('');
  const nav = useNavigate();
  const ql = q.trim().toLowerCase();
  const rows = (data ?? []).filter((a) => !ql || [a.name, a.email, ...a.users.map((u) => u.username)].some((x) => x.toLowerCase().includes(ql)));
  return (
    <>
      <div className="head">
        <div><h1>Accounts</h1><div className="sub">Each hosting customer is an account with its own logins and sites. Customers only ever see their own sites.</div></div>
        <button className="btn p" onClick={() => setAdding(true)}><Icon name="plus" />New account</button>
      </div>
      <ErrorBox error={error} />
      <div className="card">
        {data && data.length > 6 && <div className="cb" style={{ borderBottom: '1px solid var(--line)' }}><input className="input" placeholder="Filter accounts" value={q} onChange={(e) => setQ(e.target.value)} /></div>}
        {!data ? <Loading /> : (
          <table>
            <thead><tr><th>Account</th><th>Logins</th><th>Sites</th><th>Plan</th><th>Created</th><th>Status</th></tr></thead>
            <tbody>{rows.map((a) => (
              <tr key={a.id} className="click" onClick={() => nav('/accounts/' + a.id)}>
                <td><div className="row" style={{ gap: 12, alignItems: 'center' }}><SiteAvatar name={a.name} /><div><b>{a.name}</b><div className="dim" style={{ fontSize: 12 }}>{a.email || '—'}</div></div></div></td>
                <td className="muted">{a.users.map((u) => u.username).join(', ') || '—'}</td>
                <td>{a.sites.length}</td>
                <td className="muted">{a.usage?.plan?.name ?? '—'}</td>
                <td className="muted">{ago(a.created_at)}</td>
                <td>{a.suspended ? <Badge kind="b">Suspended</Badge> : a.users.some((u) => u.role === 'admin') ? <Badge kind="v">Admin</Badge> : <Badge kind="g">Active</Badge>}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </div>
      {adding && <NewAccount onClose={() => setAdding(false)} onDone={(id) => { setAdding(false); reload(); nav('/accounts/' + id); }} />}
    </>
  );
}

function NewAccount({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const [f, setF] = useState({ name: '', email: '', notes: '', username: '', password: '', plan_id: '' });
  const plans = useLoad(() => get<Plan[]>('/plans'));
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const set = (p: Partial<typeof f>) => setF({ ...f, ...p });
  const save = async () => {
    setErr(null); setBusy(true);
    try {
      const a = await post<AccountView>('/accounts', { name: f.name, email: f.email, notes: f.notes, plan_id: f.plan_id, user: { username: f.username, email: f.email, password: f.password } });
      toast('Account created');
      onDone(a.id);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <Modal title="New account" onClose={onClose}>
      <ErrorBox error={err} />
      <Field label="Name" help="The customer or company name."><input className="input" autoFocus value={f.name} onChange={(e) => set({ name: e.target.value })} /></Field>
      <Field label="Contact email" help="Also the default admin email for their new WordPress sites."><input className="input" type="email" value={f.email} onChange={(e) => set({ email: e.target.value })} /></Field>
      <div className="grid2">
        <Field label="Username"><input className="input mono" autoComplete="off" value={f.username} onChange={(e) => set({ username: e.target.value })} /></Field>
        <Field label="Password" help="At least 10 characters. Give it to the customer; they can change it."><input className="input mono" autoComplete="new-password" value={f.password} onChange={(e) => set({ password: e.target.value })} /></Field>
      </div>
      {(plans.data?.length ?? 0) > 0 && <Field label="Plan"><select className="input" value={f.plan_id} onChange={(e) => set({ plan_id: e.target.value })}>
        <option value="">No plan (no limits)</option>{plans.data!.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>}
      <Field label="Notes (only admins see these)"><textarea className="input" rows={2} value={f.notes} onChange={(e) => set({ notes: e.target.value })} /></Field>
      <div className="row" style={{ justifyContent: 'flex-end' }}><button className="btn" onClick={onClose}>Cancel</button><button className="btn p" disabled={busy} onClick={save}>Create account</button></div>
    </Modal>
  );
}

// AccountPage shows and edits one account: details, logins and sites.
export function AccountPage() {
  const { id = '' } = useParams();
  const { me } = useMe();
  const { data, error, reload } = useLoad(() => get<AccountView>('/accounts/' + id), [id]);
  const sites = useLoad(() => get<SiteRow[]>('/sites?account=' + encodeURIComponent(id)), [id]);
  const plans = useLoad(() => get<Plan[]>('/plans'));
  const [f, setF] = useState<{ name: string; email: string; notes: string; suspended: boolean; plan_id: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [userModal, setUserModal] = useState<User | 'new' | null>(null);
  const toast = useToast();
  const nav = useNavigate();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const form = f ?? { name: data.name, email: data.email, notes: data.notes, suspended: data.suspended, plan_id: data.plan_id ?? '' };
  const set = (p: Partial<typeof form>) => setF({ ...form, ...p });
  const own = data.id === me?.user.account_id;
  const go = (fn: () => Promise<unknown>, ok: string) => async () => {
    setErr(null);
    try { await fn(); toast(ok); setF(null); reload(); } catch (e) { setErr(e); }
  };
  return (
    <>
      <div className="head">
        <div><h1>{data.name}</h1><div className="sub">{data.sites.length} site{data.sites.length === 1 ? '' : 's'} · {data.users.length} login{data.users.length === 1 ? '' : 's'}{data.suspended ? ' · suspended' : ''}</div></div>
        <div className="row"><Link to="/accounts" className="btn"><Icon name="back" size={14} />Accounts</Link>
          {!own && data.users.some((u) => u.role !== 'admin') && !data.suspended && <button className="btn" title="See the panel as this customer sees it"
            onClick={() => post('/accounts/' + id + '/sign-in-as', {}).then(() => { window.location.href = '/'; }).catch((e) => setErr(e))}><Icon name="user" size={14} />Sign in as customer</button>}
          <button className="btn p" onClick={go(() => put('/accounts/' + id, form), 'Account saved')}>Save</button></div>
      </div>
      <ErrorBox error={err} />
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Details</h3></div><div className="cb">
            <Field label="Name"><input className="input" value={form.name} onChange={(e) => set({ name: e.target.value })} /></Field>
            <Field label="Contact email"><input className="input" type="email" value={form.email} onChange={(e) => set({ email: e.target.value })} /></Field>
            <Field label="Plan" help="Limits for the account's sites. Changing it applies at once.">
              <select className="input" value={form.plan_id} onChange={(e) => set({ plan_id: e.target.value })}>
                <option value="">No plan (no limits)</option>
                {plans.data?.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
            {data.external_id && <Field label="Billing system ID" help="Set through the provisioning API."><input className="input mono" value={data.external_id} readOnly /></Field>}
            <Field label="Notes (only admins see these)"><textarea className="input" rows={3} value={form.notes} onChange={(e) => set({ notes: e.target.value })} /></Field>
            {!own && <Toggle on={form.suspended} onChange={(v) => set({ suspended: v })} label="Suspended" help="Suspended accounts are signed out and cannot sign in. Their sites keep running." />}
          </div></div>
          <div className="card"><div className="ch"><h3>Logins</h3><button className="btn s" onClick={() => setUserModal('new')}><Icon name="plus" size={13} />Add login</button></div>
            <table><tbody>{data.users.map((u) => (
              <tr key={u.id} className="click" onClick={() => setUserModal(u)}>
                <td><b className="mono">{u.username}</b><div className="dim" style={{ fontSize: 12 }}>{u.email || '—'}</div></td>
                <td>{u.role === 'admin' ? <Badge kind="v">Admin</Badge> : <Badge kind="n">Customer</Badge>}</td>
                <td style={{ textAlign: 'right' }}>{u.totp_enabled ? <Badge kind="g">2FA</Badge> : <span className="dim">no 2FA</span>}</td>
              </tr>
            ))}</tbody></table>
          </div>
          {!own && data.sites.length === 0 && <div className="card"><div className="ch"><h3>Delete account</h3></div><div className="cb">
            <p className="muted" style={{ marginBottom: 12 }}>Removes the account and its logins.</p>
            <button className="btn danger" onClick={() => window.confirm(`Delete ${data.name}?`) && go(() => del('/accounts/' + id).then(() => nav('/accounts')), 'Account deleted')()}>Delete account</button>
          </div></div>}
        </div>
        <div className="stack">
        <UsageCard u={data.usage} />
        <div className="card"><div className="ch"><h3>Sites</h3><Link to={'/sites/new?account=' + id} className="btn s"><Icon name="plus" size={13} />New site</Link></div>
          {!sites.data ? <Loading /> : sites.data.length === 0 ? <div className="empty">No sites yet.</div> :
            <table><tbody>{sites.data.map((s) => (
              <tr key={s.id} className="click" onClick={() => nav('/sites/' + s.id)}>
                <td><div className="row" style={{ gap: 10, alignItems: 'center' }}><SiteAvatar name={s.domain} small /><b>{s.domain}</b></div></td>
                <td style={{ textAlign: 'right' }}><StatusBadge status={s.status} enabled={s.enabled} /></td>
              </tr>
            ))}</tbody></table>}
        </div>
        </div>
      </div>
      {userModal && <UserModal accountID={id} user={userModal === 'new' ? null : userModal} self={userModal !== 'new' && userModal.id === me?.user.id}
        onClose={() => setUserModal(null)} onDone={() => { setUserModal(null); reload(); }} />}
    </>
  );
}

function UserModal({ accountID, user, self, onClose, onDone }: { accountID: string; user: User | null; self: boolean; onClose: () => void; onDone: () => void }) {
  const [f, setF] = useState({ username: user?.username ?? '', email: user?.email ?? '', password: '', role: user?.role ?? 'customer', reset_totp: false });
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  const set = (p: Partial<typeof f>) => setF({ ...f, ...p });
  const go = (fn: () => Promise<unknown>, ok: string) => async () => {
    setErr(null);
    try { await fn(); toast(ok); onDone(); } catch (e) { setErr(e); }
  };
  const save = user
    ? () => put('/users/' + user.id, { email: f.email, password: f.password, role: f.role, reset_totp: f.reset_totp })
    : () => post('/accounts/' + accountID + '/users', { username: f.username, email: f.email, password: f.password, role: f.role });
  return (
    <Modal title={user ? 'Login ' + user.username : 'Add login'} onClose={onClose}>
      <ErrorBox error={err} />
      {!user && <Field label="Username"><input className="input mono" autoFocus autoComplete="off" value={f.username} onChange={(e) => set({ username: e.target.value })} /></Field>}
      <Field label="Email"><input className="input" type="email" value={f.email} onChange={(e) => set({ email: e.target.value })} /></Field>
      <Field label={user ? 'New password (blank keeps it)' : 'Password'}><input className="input mono" autoComplete="new-password" value={f.password} onChange={(e) => set({ password: e.target.value })} /></Field>
      {!self && <Field label="Role" help="Admins run the whole server and see every account. Give it only to people you trust with root.">
        <select className="input" value={f.role} onChange={(e) => set({ role: e.target.value as User['role'] })}><option value="customer">Customer</option><option value="admin">Admin</option></select></Field>}
      {user?.totp_enabled && <Toggle on={f.reset_totp} onChange={(v) => set({ reset_totp: v })} label="Turn off two-factor" help="For a customer who lost their authenticator." />}
      <div className="row" style={{ justifyContent: 'space-between', marginTop: 8 }}>
        {user && !self ? <button className="btn danger" onClick={() => window.confirm(`Remove ${user.username}?`) && go(() => del('/users/' + user.id), 'Login removed')()}>Remove</button> : <span />}
        <div className="row"><button className="btn" onClick={onClose}>Cancel</button><button className="btn p" onClick={go(save, user ? 'Login saved' : 'Login added')}>{user ? 'Save' : 'Add login'}</button></div>
      </div>
    </Modal>
  );
}
