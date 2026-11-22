import { useEffect, useState } from 'react';
import { useResponsiveTables } from './responsive';
import { BrowserRouter, Link, Navigate, NavLink, Route, Routes, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { get, PluginView, post, setUnauthorizedHandler, SiteDetail, SiteRow } from './api';
import { Icon, Logo, PageBoundary, SiteAvatar, ToastProvider } from './ui';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Sites from './pages/Sites';
import NewSite from './pages/NewSite';
import SitePage, { tabIcon, tabLabel, visibleTabs } from './pages/Site';
import Backups from './pages/Backups';
import Activity from './pages/Activity';
import System from './pages/System';
import Settings from './pages/Settings';
import Plugins, { PluginPage, pluginPages } from './pages/Plugins';
import Accounts, { AccountPage } from './pages/Accounts';
import Plans from './pages/Plans';
import MyAccount from './pages/MyAccount';
import { Me, MeProvider } from './me';

export type { Me };

// Enabled plugins with a page add sidebar entries after Sites.
// Customers see their hosting and their account; admins also run the platform.
const buildNav = (plugins: PluginView[], admin: boolean) => [
  ['Overview'], ['/', 'dash', 'Dashboard'],
  ['Hosting'], ['/sites', 'site', 'Sites'],
  ...plugins.filter((p) => p.enabled && p.nav_label && pluginPages[p.id]).map((p) => ['/p/' + p.id, p.icon || 'plug', p.nav_label]),
  ['/backups', 'backup', 'Backups'], ['/activity', 'pulse', 'Activity'],
  ...(admin
    ? [['Platform'], ['/accounts', 'users', 'Accounts'], ['/plans', 'layers', 'Plans'], ['/system', 'server', 'System'], ['/plugins', 'plug', 'Plugins'], ['/settings', 'set', 'Settings']]
    : [['Account'], ['/account', 'set', 'My account']]),
];

const titles: Record<string, string> = {
  '': 'Dashboard', sites: 'Sites', backups: 'Backups', activity: 'Activity', system: 'System', settings: 'Settings', plugins: 'Plugins',
  accounts: 'Accounts', account: 'My account', plans: 'Plans',
};

function PluginRoute() {
  const { id } = useParams();
  return <PluginPage id={id ?? ''} />;
}

// SiteNav replaces the main navigation while a site is open, like a hosting
// dashboard's per-site menu.
function SiteNav({ id }: { id: string }) {
  const [sp] = useSearchParams();
  const [d, setD] = useState<SiteDetail | null>(null);
  useEffect(() => {
    let alive = true;
    const load = () => get<SiteDetail>('/sites/' + id).then((r) => alive && setD(r)).catch(() => undefined);
    load();
    const t = window.setInterval(load, 30000);
    return () => { alive = false; window.clearInterval(t); };
  }, [id]);
  const tab = sp.get('tab') || 'overview';
  return (
    <>
      <Link to="/sites" className="sideback"><Icon name="back" size={14} />All sites</Link>
      {d && <div className="sitecard"><SiteAvatar name={d.site.domain} small /><div><b>{d.site.domain}</b><span>{d.uptime?.down ? 'Down' : d.site.enabled ? 'Live' : 'Offline'} · PHP {d.site.php_version}</span></div></div>}
      <nav className="nav">
        <div className="navlabel">Site</div>
        {d && visibleTabs(d.site).map((t) => (
          <Link key={t} to={t === 'overview' ? '/sites/' + id : `/sites/${id}?tab=${t}`} className={tab === t ? 'on' : ''}>
            <Icon name={tabIcon[t]} />{tabLabel[t]}
            {t === 'wordpress' && d.vulns > 0 && <span className="cnt w">{d.vulns}</span>}
          </Link>
        ))}
      </nav>
    </>
  );
}

// SiteSearch finds a site by domain or name and opens it.
function SiteSearch() {
  const [q, setQ] = useState('');
  const [sites, setSites] = useState<SiteRow[] | null>(null);
  const [sel, setSel] = useState(0);
  const nav = useNavigate();
  const ql = q.trim().toLowerCase();
  const hits = !ql || !sites ? [] : sites.filter((s) => [s.domain, s.name, ...(s.aliases ?? [])].some((x) => x.includes(ql))).slice(0, 8);
  const go = (id: string) => { setQ(''); nav('/sites/' + id); };
  return (
    <div className="search">
      <Icon name="search" size={15} />
      <input value={q} placeholder="Search sites" aria-label="Search sites"
        onFocus={() => !sites && get<SiteRow[]>('/sites').then(setSites).catch(() => undefined)}
        onChange={(e) => { setQ(e.target.value); setSel(0); }}
        onBlur={() => window.setTimeout(() => setQ(''), 150)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') { e.preventDefault(); setSel((x) => Math.min(x + 1, hits.length - 1)); }
          if (e.key === 'ArrowUp') { e.preventDefault(); setSel((x) => Math.max(x - 1, 0)); }
          if (e.key === 'Enter' && hits[sel]) go(hits[sel].id);
          if (e.key === 'Escape') setQ('');
        }} />
      {ql && <div className="results">
        {hits.length === 0 ? <div className="dim" style={{ padding: 10 }}>No site matches.</div> : hits.map((s, i) => (
          <a key={s.id} className={i === sel ? 'on' : ''} onMouseDown={(e) => { e.preventDefault(); go(s.id); }}>
            <SiteAvatar name={s.domain} small /><div><b>{s.domain}</b><small>{s.name}</small></div>
          </a>
        ))}
      </div>}
    </div>
  );
}

function Shell({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const loc = useLocation();
  const section = loc.pathname.split('/')[1] || '';
  const [menu, setMenu] = useState(false);
  useEffect(() => setMenu(false), [loc.pathname]);
  useEffect(() => {
    if (!menu) return;
    const h = (e: KeyboardEvent) => e.key === 'Escape' && setMenu(false);
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [menu]);
  useResponsiveTables();
  const admin = me.user.role === 'admin';
  const [plugins, setPlugins] = useState<PluginView[]>([]);
  useEffect(() => {
    if (!admin) { setPlugins(me.plugins.map((p) => ({ ...p, enabled: true } as PluginView))); return; }
    const load = () => get<PluginView[]>('/plugins').then(setPlugins).catch(() => undefined);
    load();
    window.addEventListener('berth-plugins', load);
    return () => window.removeEventListener('berth-plugins', load);
  }, []);
  const nav = buildNav(plugins, admin);
  const title = section === 'p' ? plugins.find((p) => p.id === loc.pathname.split('/')[2])?.nav_label ?? '' : titles[section] ?? '';
  const siteID = section === 'sites' ? loc.pathname.split('/')[2] : '';
  const inSite = !!siteID && siteID !== 'new';
  const [newer, setNewer] = useState('');
  useEffect(() => {
    if (!admin) return;
    const check = () => get<{ update: { current: string; latest: string } }>('/system')
      .then((r) => setNewer(r.update.latest && r.update.latest !== r.update.current && r.update.current !== 'dev' ? r.update.latest : ''))
      .catch(() => undefined);
    check();
    const t = window.setInterval(check, 30 * 60 * 1000);
    return () => window.clearInterval(t);
  }, [loc.pathname === '/system']);
  return (
    <>
      {menu && <div className="scrim" onClick={() => setMenu(false)} />}
      <aside className={'side' + (menu ? ' open' : '')}>
        <div className="brand"><Logo />Berth<span className="badge n" style={{ marginLeft: 'auto', fontSize: 10 }}>{me.version}</span></div>
        {inSite ? <SiteNav id={siteID} /> : <nav className="nav">
          {nav.map((n) => n.length === 1
            ? <div key={n[0]} className="navlabel">{n[0]}</div>
            : <NavLink key={n[0]} to={n[0]} end={n[0] === '/'} className={({ isActive }) => (isActive ? 'on' : '')}>
                <Icon name={n[1]} />{n[2]}
                {n[0] === '/system' && newer && <span className="badge w" style={{ marginLeft: 'auto', fontSize: 10 }} title={'Berth ' + newer + ' is available'}>Update</span>}
              </NavLink>)}
        </nav>}
        <div className="foot">
          <div className="node"><span className="dot" /><b style={{ fontWeight: 650 }}>{admin ? me.name : me.account.name}</b></div>
          <div className="node" style={{ marginTop: 10 }}>
            <span className="muted">{me.user.username}</span>
            <button className="btn s ghost" style={{ marginLeft: 'auto' }} onClick={onLogout} title="Sign out"><Icon name="out" size={13} /></button>
          </div>
        </div>
      </aside>
      <div className="main">
        <div className="top">
          <button className="menubtn" onClick={() => setMenu(true)} aria-label="Open menu"><Icon name="menu" size={20} /></button>
          <div className="crumb">{inSite ? <><Link to="/sites">Sites</Link><span className="sep">/</span><b>{siteID}</b></> : siteID === 'new' ? <><Link to="/sites">Sites</Link><span className="sep">/</span><b>New site</b></> : <b>{title}</b>}</div>
          <SiteSearch />
          <Link to="/sites/new" className="btn p s hide-sm"><Icon name="plus" size={14} />New site</Link>
          <div className="avatar" title={me.user.username}>{me.user.username.slice(0, 2).toUpperCase()}</div>
        </div>
        <div className="content">
          <PageBoundary resetKey={loc.pathname + loc.search}>
          <Routes>
            <Route path="/" element={<Dashboard me={me.user.username} />} />
            <Route path="/sites" element={<Sites />} />
            <Route path="/sites/new" element={<NewSite />} />
            <Route path="/sites/:id" element={<SitePage />} />
            <Route path="/backups" element={<Backups />} />
            <Route path="/activity" element={<Activity />} />
            <Route path="/account" element={<MyAccount me={me} />} />
            {admin && <>
              <Route path="/system" element={<System />} />
              <Route path="/settings" element={<Settings me={me} />} />
              <Route path="/plugins" element={<Plugins />} />
              <Route path="/accounts" element={<Accounts />} />
              <Route path="/plans" element={<Plans />} />
              <Route path="/accounts/:id" element={<AccountPage />} />
            </>}
            <Route path="/p/:id" element={<PluginRoute />} />
            <Route path="/p/:id/:sub" element={<PluginRoute />} />
            <Route path="*" element={<Navigate to="/" />} />
          </Routes>
          </PageBoundary>
        </div>
      </div>
    </>
  );
}

export default function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const load = () => get<Me>('/me').then(setMe).catch(() => setMe(null));
  useEffect(() => {
    setUnauthorizedHandler(() => setMe(null));
    load();
  }, []);
  if (me === undefined) return null;
  return (
    <ToastProvider>
      <BrowserRouter>
        {me ? <MeProvider value={me}><Shell me={me} onLogout={() => post('/logout').finally(() => setMe(null))} /></MeProvider> : <Login onDone={load} />}
      </BrowserRouter>
    </ToastProvider>
  );
}
