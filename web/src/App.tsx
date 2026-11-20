import { useEffect, useState } from 'react';
import { useResponsiveTables } from './responsive';
import { BrowserRouter, Navigate, NavLink, Route, Routes, useLocation } from 'react-router-dom';
import { get, post, setUnauthorizedHandler, User } from './api';
import { Icon, Logo, ToastProvider } from './ui';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Sites from './pages/Sites';
import NewSite from './pages/NewSite';
import SitePage from './pages/Site';
import Backups from './pages/Backups';
import Activity from './pages/Activity';
import System from './pages/System';
import Settings from './pages/Settings';

export interface Me { user: User; name: string; version: string }

const nav = [
  ['Overview'], ['/', 'dash', 'Dashboard'],
  ['Hosting'], ['/sites', 'site', 'Sites'], ['/backups', 'backup', 'Backups'], ['/activity', 'pulse', 'Activity'],
  ['Platform'], ['/system', 'server', 'System'], ['/settings', 'set', 'Settings'],
];

const titles: Record<string, string> = {
  '': 'Dashboard', sites: 'Sites', backups: 'Backups', activity: 'Activity', system: 'System', settings: 'Settings',
};

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
  return (
    <>
      {menu && <div className="scrim" onClick={() => setMenu(false)} />}
      <aside className={'side' + (menu ? ' open' : '')}>
        <div className="brand"><Logo />Berth<span className="badge n" style={{ marginLeft: 'auto', fontSize: 10 }}>{me.version}</span></div>
        <nav className="nav">
          {nav.map((n) => n.length === 1
            ? <div key={n[0]} className="navlabel">{n[0]}</div>
            : <NavLink key={n[0]} to={n[0]} end={n[0] === '/'} className={({ isActive }) => (isActive ? 'on' : '')}>
                <Icon name={n[1]} />{n[2]}
              </NavLink>)}
        </nav>
        <div className="foot">
          <div className="node"><span className="dot" /><b style={{ color: '#e6edf6', fontWeight: 600 }}>{me.name}</b></div>
          <div className="node" style={{ marginTop: 10 }}>
            <span className="muted">{me.user.username}</span>
            <button className="btn s ghost" style={{ marginLeft: 'auto' }} onClick={onLogout} title="Sign out"><Icon name="out" size={13} /></button>
          </div>
        </div>
      </aside>
      <div className="main">
        <div className="top">
          <button className="menubtn" onClick={() => setMenu(true)} aria-label="Open menu"><Icon name="menu" size={20} /></button>
          <div className="crumb"><b>{titles[section] ?? ''}</b></div>
          <div className="avatar" style={{ marginLeft: 'auto' }} title={me.user.username}>{me.user.username.slice(0, 2).toUpperCase()}</div>
        </div>
        <div className="content">
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/sites" element={<Sites />} />
            <Route path="/sites/new" element={<NewSite />} />
            <Route path="/sites/:id" element={<SitePage />} />
            <Route path="/backups" element={<Backups />} />
            <Route path="/activity" element={<Activity />} />
            <Route path="/system" element={<System />} />
            <Route path="/settings" element={<Settings me={me} />} />
            <Route path="*" element={<Navigate to="/" />} />
          </Routes>
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
        {me ? <Shell me={me} onLogout={() => post('/logout').finally(() => setMe(null))} /> : <Login onDone={load} />}
      </BrowserRouter>
    </ToastProvider>
  );
}
