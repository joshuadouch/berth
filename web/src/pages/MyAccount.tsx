import type { Me } from '../me';
import { UsageCard } from './Plans';
import { AccountCard } from './Settings';

export default function MyAccount({ me }: { me: Me }) {
  return (
    <>
      <div className="head"><div><h1>My account</h1><div className="sub">{me.account.name} · signed in as {me.user.username}</div></div></div>
      <div className="grid2" style={{ alignItems: 'start' }}>
        <AccountCard me={me} />
        <div className="card"><div className="ch"><h3>Hosting account</h3></div><div className="cb">
          <table><tbody>
            <tr><td className="muted">Account</td><td><b>{me.account.name}</b></td></tr>
            <tr><td className="muted">Contact email</td><td>{me.account.email || me.user.email || '—'}</td></tr>
            <tr><td className="muted">Username</td><td className="mono">{me.user.username}</td></tr>
          </tbody></table>
          <small className="help">To change your plan or contact details, get in touch with your host.</small>
        </div></div>
        {me.usage && <UsageCard u={me.usage} title="Your plan" />}
      </div>
    </>
  );
}
