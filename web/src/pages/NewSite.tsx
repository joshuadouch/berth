import { FormEvent, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useMe } from '../me';
import { AccountView, bytes, del, exposureLabel, get, HalyardInfo, Job, nameFromDomain, post, Settings, Site, SiteType, upload, UploadInfo, vipLabel } from '../api';
import { Chips, Copyable, ErrorBox, Field, Icon, JobView, Loading, Toggle, useLoad } from '../ui';

const types: { v: SiteType; icon: string; title: string; text: string }[] = [
  { v: 'wordpress', icon: 'wp', title: 'WordPress', text: 'Installed and ready to log in, with its own database.' },
  { v: 'php_db', icon: 'db', title: 'PHP + database', text: 'A PHP site or app with its own MariaDB database.' },
  { v: 'php', icon: 'php', title: 'PHP', text: 'PHP or static files, no database.' },
];

interface Result { site: Site; job_id: number; admin_password?: string }

export default function NewSite() {
  const cfg = useLoad(() => get<{ settings: Settings; installed_php: string[] }>('/settings'));
  const nav = useNavigate();
  const [type, setType] = useState<SiteType>('wordpress');
  const [domain, setDomain] = useState('');
  const [name, setName] = useState('');
  const [nameTouched, setNameTouched] = useState(false);
  const [aliases, setAliases] = useState<string[]>([]);
  const [redirect, setRedirect] = useState(true);
  const [php, setPhp] = useState('');
  const [https, setHttps] = useState(true);
  const [title, setTitle] = useState('');
  const [adminUser, setAdminUser] = useState('');
  const [adminEmail, setAdminEmail] = useState('');
  const [locale, setLocale] = useState('');
  const [docRoot, setDocRoot] = useState('');
  const [front, setFront] = useState(true);
  const [backups, setBackups] = useState(true);
  const [adv, setAdv] = useState(false);
  const { me, isAdmin } = useMe();
  const [sp] = useSearchParams();
  const hal = useLoad(() => (isAdmin ? get<HalyardInfo>('/halyard') : Promise.resolve(null)));
  const accounts = useLoad(() => (isAdmin ? get<AccountView[]>('/accounts') : Promise.resolve([] as AccountView[])));
  const [account, setAccount] = useState(sp.get('account') || me?.user.account_id || '');
  const [publish, setPublish] = useState(true);
  const [vip, setVip] = useState('');
  const [exposure, setExposure] = useState('');
  const [dns, setDns] = useState<boolean | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<Result | null>(null);
  const [job, setJob] = useState<Job | null>(null);
  const [imp, setImp] = useState(false);
  const [impFiles, setImpFiles] = useState<UploadInfo | null>(null);
  const [impDB, setImpDB] = useState<UploadInfo | null>(null);
  const [uploading, setUploading] = useState(0);

  useEffect(() => {
    const s = cfg.data?.settings;
    if (!s) return;
    setPhp((p) => p || (cfg.data!.installed_php.includes(s.default_php) ? s.default_php : cfg.data!.installed_php.slice(-1)[0] ?? s.default_php));
    setAdminUser((v) => v || s.wp_admin_user);
    setAdminEmail((v) => v || s.wp_admin_email);
    setLocale((v) => v || s.wp_locale);
    setVip((v) => v || s.halyard.vip_id);
    setExposure((v) => v || s.halyard.exposure || 'public');
    setDns((v) => (v === null ? !s.halyard.no_dns : v));
  }, [cfg.data]);
  useEffect(() => {
    if (!nameTouched) setName(domain ? nameFromDomain(domain) : '');
  }, [domain, nameTouched]);

  const hasDB = type !== 'php';
  const importing = imp && (!!impFiles || (hasDB && !!impDB));
  const importReady = !imp || (type === 'wordpress' ? !!impFiles && !!impDB : importing);
  const halyardReady = !!hal.data?.configured && !hal.data.error && hal.data.pool_ok;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      const body: any = {
        type, domain: domain.trim(), name, aliases, redirect_aliases: redirect, php_version: php, https, enabled: true,
        doc_root: docRoot, front_controller: front, backups, sftp: { enabled: false, keys: [] },
      };
      if (isAdmin && account) body.account_id = account;
      if (halyardReady && publish) body.halyard = { enabled: true, vip_id: vip, exposure, no_dns: dns === false };
      if (type === 'wordpress') body.wp = importing ? {} : { title: title || domain.trim(), admin_user: adminUser, admin_email: adminEmail, locale };
      if (importing) body.import = { files: impFiles?.id ?? '', files_name: impFiles?.name ?? '', db: hasDB ? impDB?.id ?? '' : '', db_name: hasDB ? impDB?.name ?? '' : '' };
      setRes(await post<Result>('/sites', body));
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };

  if (!cfg.data) return cfg.error ? <ErrorBox error={cfg.error} /> : <Loading />;

  if (res) {
    const s = res.site;
    return (
      <>
        <div className="head"><div><h1>{s.domain}</h1><div className="sub">{s.import ? 'Moving your site in.' : <>Creating your {types.find((t) => t.v === s.type)?.title} site.</>}</div></div></div>
        <div className="row" style={{ alignItems: 'flex-start' }}>
          <div className="card" style={{ flex: 3, minWidth: 0 }}><div className="cb"><JobView id={res.job_id} onDone={setJob} /></div></div>
          <div className="stack" style={{ flex: 2, minWidth: 0 }}>
            {res.admin_password && <div className="card"><div className="ch"><h3>WordPress login</h3></div><div className="cb">
              <div className="warnbox">Save this password now. Berth does not keep it; you can set a new one from the site page later.</div>
              <Field label="Username"><Copyable value={s.wp?.admin_user ?? ''} /></Field>
              <Field label="Password"><Copyable value={res.admin_password} /></Field>
              <Field label="Log in at"><Copyable value={(s.https ? 'https://' : 'http://') + s.domain + '/wp-admin/'} /></Field>
            </div></div>}
            {s.import && s.type === 'wordpress' && <div className="card"><div className="ch"><h3>WordPress login</h3></div><div className="cb">
              <p>Log in with the accounts you already had. Berth pointed wp-config.php at the new database and changed the old address to <b>{s.domain}</b>.</p>
              <Field label="Log in at"><Copyable value={(s.https ? 'https://' : 'http://') + s.domain + '/wp-admin/'} /></Field>
            </div></div>}
            {job && <div className="card"><div className="cb">
              {job.status === 'ok' ? <p style={{ marginBottom: 12 }}>The site is ready. {res?.site.halyard?.enabled ? <>It is published on Halyard; the site page shows the host's status.</> : <>Point <b>{s.domain}</b> at Halyard if you have not already.</>}</p> : <p style={{ marginBottom: 12 }}>Something went wrong. The log shows where; you can retry from the site page.</p>}
              <button className="btn p" onClick={() => nav('/sites/' + s.id)}>Open site</button>
            </div></div>}
          </div>
        </div>
      </>
    );
  }

  const installed = cfg.data.installed_php;
  return (
    <>
      <div className="head"><div><h1>New site</h1><div className="sub">Berth creates the system user, PHP pool, nginx site and database for you.</div></div>
        <Link to="/sites" className="btn ghost">Cancel</Link></div>
      <form onSubmit={submit} style={{ maxWidth: 860 }}>
        <div className="typecards">
          {types.map((t) => (
            <button type="button" key={t.v} className={'typecard' + (type === t.v ? ' on' : '')} onClick={() => setType(t.v)}>
              <b><Icon name={t.icon} />{t.title}</b><span>{t.text}</span>
            </button>
          ))}
        </div>
        <ErrorBox error={err} />
        <div className="card" style={{ marginBottom: 16 }}><div className="cb">
          <Toggle on={imp} onChange={setImp} label="Move an existing site here" help="Upload the site's files and its database dump. Berth puts them in place, imports the database and, for WordPress, repoints wp-config.php and the site address." />
          {imp && <>
            <div className="grid2" style={{ marginTop: 4 }}>
              <UploadSlot label="Site files" accept=".zip,.tar,.tar.gz,.tgz" hint="A .zip, .tar or .tar.gz. A cPanel or Plesk backup works; Berth finds public_html inside it."
                value={impFiles} onChange={setImpFiles} onBusy={(b) => setUploading((n) => n + (b ? 1 : -1))} />
              {hasDB ? <UploadSlot label="Database dump" accept=".sql,.gz" hint="A .sql or .sql.gz from mysqldump, phpMyAdmin or a host's backup tool."
                value={impDB} onChange={setImpDB} onBusy={(b) => setUploading((n) => n + (b ? 1 : -1))} />
                : <div className="help" style={{ alignSelf: 'center' }}>A PHP site has no database. Choose PHP + database to bring one.</div>}
            </div>
            {type === 'wordpress' && (!impFiles || !impDB) && <small className="help">WordPress needs both the files and the database.</small>}
            {type === 'wordpress' && <small className="help">Leave wp-config.php in the files; Berth updates its database settings. Logins and passwords stay as they were.</small>}
          </>}
        </div></div>
        <div className="card"><div className="cb">
          <div className="grid2">
            <Field label="Domain"><input className="input" autoFocus placeholder="blog.example.com" value={domain} onChange={(e) => setDomain(e.target.value.trim().toLowerCase())} /></Field>
            <Field label="Short name" help="Used for the system user, folder and database. Cannot change later.">
              <input className="input mono" value={name} onChange={(e) => { setNameTouched(true); setName(e.target.value.toLowerCase()); }} /></Field>
          </div>
          <Field label="Other domains (optional)" help="Also served by this site, such as the www. name.">
            <Chips values={aliases} onChange={setAliases} placeholder={domain ? 'www.' + domain : 'www.example.com'} mono />
          </Field>
          {aliases.length > 0 && <Toggle on={redirect} onChange={setRedirect} label="Redirect other domains to the main domain" help="Visitors on another domain are sent to the main one with a 301." />}
          <div className="grid2">
            <Field label="PHP version">
              <select className="input" value={php} onChange={(e) => setPhp(e.target.value)}>
                {installed.map((v) => <option key={v} value={v}>PHP {v}</option>)}
              </select>
            </Field>
            <Field label="Visitors use">
              <select className="input" value={https ? 'https' : 'http'} onChange={(e) => setHttps(e.target.value === 'https')}>
                <option value="https">{isAdmin ? 'HTTPS (Halyard or Cloudflare ends TLS)' : 'HTTPS'}</option>
                <option value="http">HTTP only</option>
              </select>
            </Field>
          </div>
          {isAdmin && (accounts.data?.length ?? 0) > 1 && <Field label="Account" help="The customer who owns the site. They see it when they sign in.">
            <select className="input" value={account} onChange={(e) => setAccount(e.target.value)}>
              {accounts.data!.map((a) => <option key={a.id} value={a.id}>{a.name}{a.id === me?.user.account_id ? ' (you)' : ''}</option>)}</select></Field>}
          {halyardReady && <>
            <Toggle on={publish} onChange={setPublish} label="Publish on Halyard" help={https && cfg.data?.settings.halyard.auto_cert ? 'Adds a Halyard host for these domains and requests a certificate if none covers them.' : 'Adds a Halyard host for these domains.'} />
            {publish && <div className="grid2">
              <Field label="VIP"><select className="input" value={vip} onChange={(e) => setVip(e.target.value)}>
                <option value="">Choose a VIP</option>
                {hal.data!.vips.map((v) => <option key={v.id} value={v.id}>{vipLabel(v)}</option>)}</select></Field>
              <Field label="Exposure"><select className="input" value={exposure} onChange={(e) => setExposure(e.target.value)}>
                {Object.entries(exposureLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></Field>
            </div>}
            {publish && <Toggle on={dns !== false} onChange={setDns} label="Manage DNS in Cloudflare" help={exposure === 'lan' ? 'A plain record pointing at the VIP.' : 'A proxied (orange cloud) record for each domain.'} />}
          </>}
          {type === 'wordpress' && !imp && <>
            <div className="navlabel" style={{ padding: '6px 0 12px' }}>WordPress</div>
            <Field label="Site title"><input className="input" placeholder={domain || 'My site'} value={title} onChange={(e) => setTitle(e.target.value)} /></Field>
            <div className="grid3">
              <Field label="Admin username"><input className="input" value={adminUser} onChange={(e) => setAdminUser(e.target.value)} /></Field>
              <Field label="Admin email"><input className="input" type="email" value={adminEmail} onChange={(e) => setAdminEmail(e.target.value)} /></Field>
              <Field label="Language"><input className="input mono" value={locale} onChange={(e) => setLocale(e.target.value)} placeholder="en_US" /></Field>
            </div>
            <small className="help" style={{ marginTop: -8, marginBottom: 14 }}>A strong admin password is generated and shown once when the site is ready.</small>
          </>}
          <button type="button" className="btn s ghost" onClick={() => setAdv(!adv)} style={{ marginBottom: adv ? 14 : 0 }}>{adv ? 'Hide' : 'Show'} advanced options</button>
          {adv && <>
            {type !== 'wordpress' && <>
              <Field label="Document root inside public (optional)" help="For apps that serve from a subfolder, like Laravel's public or Bedrock's web.">
                <input className="input mono" placeholder="public" value={docRoot} onChange={(e) => setDocRoot(e.target.value)} /></Field>
              <Toggle on={front} onChange={setFront} label="Send unknown paths to index.php" help="Needed by most PHP apps and frameworks with pretty URLs." />
            </>}
            <Toggle on={backups} onChange={setBackups} label="Include in nightly backups" />
          </>}
        </div></div>
        <div style={{ marginTop: 16 }}>
          <button className="btn p" disabled={busy || !domain || !php || uploading > 0 || !importReady}><Icon name={type === 'wordpress' ? 'wp' : 'plus'} />{imp ? 'Create site and import' : type === 'wordpress' ? 'Create WordPress site' : 'Create site'}</button>
          {uploading > 0 && <span className="dim" style={{ marginLeft: 10, fontSize: 13 }}>Waiting for the upload to finish…</span>}
        </div>
      </form>
    </>
  );
}

function UploadSlot({ label, hint, accept, value, onChange, onBusy }: { label: string; hint: string; accept: string; value: UploadInfo | null; onChange: (v: UploadInfo | null) => void; onBusy: (b: boolean) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [pct, setPct] = useState<number | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const pick = async (f?: File) => {
    if (!f) return;
    setErr(null); setPct(0); onBusy(true);
    try {
      const info = await upload<UploadInfo>('/imports', [{ file: f, name: f.name }], setPct);
      if (value) del('/imports/' + value.id).catch(() => undefined);
      onChange(info);
    } catch (e) { setErr(e); } finally { setPct(null); onBusy(false); }
  };
  const remove = () => { if (value) del('/imports/' + value.id).catch(() => undefined); onChange(null); };
  return (
    <Field label={label} help={hint}>
      <div className="upslot" onDragOver={(e) => e.preventDefault()} onDrop={(e) => { e.preventDefault(); pick(e.dataTransfer.files[0]); }}>
        {pct !== null ? <div style={{ flex: 1 }}><div className="bar"><i style={{ width: pct + '%', background: 'var(--accent)' }} /></div><small className="help">Uploading… {pct}%</small></div>
          : value ? <><Icon name={value.kind === 'db' ? 'db' : 'zip'} size={15} /><span className="mono" style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>{value.name}</span><span className="dim">{bytes(value.size)}</span>
            <button type="button" className="btn s ghost" title="Remove" onClick={remove}><Icon name="trash" size={13} /></button></>
          : <><button type="button" className="btn s" onClick={() => input.current?.click()}><Icon name="up" size={13} />Choose file</button><span className="dim">or drop it here</span></>}
        <input ref={input} type="file" hidden accept={accept} onChange={(e) => { pick(e.target.files?.[0]); e.target.value = ''; }} />
      </div>
      <ErrorBox error={err} />
    </Field>
  );
}
