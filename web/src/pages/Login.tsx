import { FormEvent, useEffect, useState } from 'react';
import { get, post } from '../api';
import { BrandMark, ErrorBox, Field, useBrandTheme, useFavicon } from '../ui';

export default function Login({ onDone }: { onDone: () => void }) {
  const [setup, setSetup] = useState<{ needs_setup: boolean; name: string; brand: string; logo?: string; favicon?: string; accent?: string; sidebar?: string; access_enabled: boolean } | null>(null);
  const expired = new URLSearchParams(window.location.search).get('link') === 'expired';
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [password2, setPassword2] = useState('');
  const [totp, setTotp] = useState('');
  const [needTotp, setNeedTotp] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => { get('/setup').then(setSetup).catch(setErr); }, []);
  useFavicon(setup?.favicon);
  useBrandTheme(setup?.accent, setup?.sidebar);
  useEffect(() => { if (setup?.brand) document.title = setup.brand; }, [setup?.brand]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setErr(null);
    if (setup?.needs_setup && password !== password2) { setErr(new Error('Passwords do not match')); return; }
    setBusy(true);
    try {
      if (setup?.needs_setup) await post('/setup', { username, email, password });
      else await post('/login', { username, password, totp });
      onDone();
    } catch (e: any) {
      if (e?.data?.totp_required) setNeedTotp(true);
      setErr(e);
    } finally { setBusy(false); }
  };

  if (!setup) return <div className="login"><ErrorBox error={err} /></div>;
  return (
    <div className="login">
      <div className="brand" style={{ justifyContent: 'center', fontSize: 20, padding: '0 0 24px' }}><BrandMark logo={setup.logo} name={setup.brand} /></div>
      <form className="card" onSubmit={submit}>
        <div className="ch"><h3>{setup.needs_setup ? 'Create the first admin' : `Sign in to ${setup.brand || setup.name}`}</h3></div>
        <div className="cb">
          <ErrorBox error={err} />
          {expired && !err && <div className="note">That sign-in link has expired or was already used. Sign in with your username and password, or ask for a new link.</div>}
          {setup.needs_setup && <div className="note">This account manages every site on this server. Your email is also used as the default WordPress admin email, and for Cloudflare Access sign-in if you set it up later.</div>}
          <Field label="Username"><input className="input" autoFocus value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" /></Field>
          {setup.needs_setup && <Field label="Email"><input className="input" value={email} onChange={(e) => setEmail(e.target.value)} type="email" /></Field>}
          <Field label="Password"><input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete={setup.needs_setup ? 'new-password' : 'current-password'} /></Field>
          {setup.needs_setup && <Field label="Confirm password" help="At least 10 characters."><input className="input" type="password" value={password2} onChange={(e) => setPassword2(e.target.value)} autoComplete="new-password" /></Field>}
          {needTotp && <Field label="Authenticator code"><input className="input mono" inputMode="numeric" autoFocus value={totp} onChange={(e) => setTotp(e.target.value)} autoComplete="one-time-code" /></Field>}
          <button className="btn p" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>{setup.needs_setup ? 'Create admin' : 'Sign in'}</button>
          {setup.access_enabled && !setup.needs_setup && <small className="help" style={{ textAlign: 'center' }}>Reaching this page through your Cloudflare Access hostname signs you in automatically.</small>}
        </div>
      </form>
    </div>
  );
}
