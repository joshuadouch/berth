import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { ago, bytes, get, SiteRow } from '../api';
import { ErrorBox, Icon, Loading, StatusBadge, TypeBadge, useLoad } from '../ui';

export default function Sites() {
  const { data, error } = useLoad(() => get<SiteRow[]>('/sites'), [], 10000);
  const [q, setQ] = useState('');
  const nav = useNavigate();
  const rows = (data ?? []).filter((s) => !q || [s.domain, s.name, ...(s.aliases ?? [])].some((x) => x.includes(q.toLowerCase())));
  return (
    <>
      <div className="head">
        <div><h1>Sites</h1><div className="sub">Every site on this server. Each has its own system user, PHP pool and database.</div></div>
        <Link to="/sites/new" className="btn p"><Icon name="plus" />New site</Link>
      </div>
      <ErrorBox error={error} />
      <div className="card">
        {data && data.length > 6 && <div className="cb" style={{ borderBottom: '1px solid var(--line)' }}><input className="input" placeholder="Filter by domain" value={q} onChange={(e) => setQ(e.target.value)} /></div>}
        {!data ? <Loading /> : data.length === 0 ? (
          <div className="empty">
            <p style={{ marginBottom: 14 }}>No sites yet.</p>
            <Link to="/sites/new" className="btn p"><Icon name="wp" />Create a WordPress site</Link>
          </div>
        ) : (
          <table>
            <thead><tr><th>Site</th><th>Type</th><th>PHP</th><th>Disk</th><th>Last backup</th><th>Status</th></tr></thead>
            <tbody>{rows.map((s) => (
              <tr key={s.id} className="click" onClick={() => nav('/sites/' + s.id)}>
                <td><b>{s.domain}</b>{(s.aliases?.length ?? 0) > 0 && <span className="dim"> +{s.aliases!.length}</span>}<div className="dim mono" style={{ fontSize: 11 }}>{s.name}</div></td>
                <td><TypeBadge type={s.type} /></td>
                <td className="mono">{s.php_version}</td>
                <td className="muted">{bytes(s.usage.files + s.usage.db)}</td>
                <td className="muted">{s.last_backup ? ago(s.last_backup.time) : s.backups ? 'none yet' : 'off'}</td>
                <td><StatusBadge status={s.status} enabled={s.enabled} /></td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </div>
    </>
  );
}
