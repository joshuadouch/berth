import { useState } from 'react';
import { ago, Disk, get, post, Service } from '../api';
import { Badge, ErrorBox, Icon, JobView, Loading, Modal, useLoad, useToast } from '../ui';
import { DiskBars, ServiceList } from './Dashboard';

interface Sys {
  name: string; version: string; update: { current: string; latest: string; latest_url: string; check_error?: string };
  php_versions: string[]; installed_php: string[]; php_in_use: Record<string, number>; services: Service[]; disks: Disk[];
  paths: Record<string, string>; sftp: boolean;
  php_updates: PHPUpdates;
}
interface PHPUpdates { checked: string; updates: { package: string; php: string; from: string; to: string }[] | null; error?: string; checking: boolean }

export default function System() {
  const [check, setCheck] = useState(0);
  const { data, error, reload } = useLoad(() => get<Sys>('/system' + (check ? '?check=1' : '')), [check], 20000);
  const [job, setJob] = useState<number | null>(null);
  const [updating, setUpdating] = useState(false);
  const [phpCheck, setPhpCheck] = useState<PHPUpdates | null>(null);
  const [phpBusy, setPhpBusy] = useState(false);
  const toast = useToast();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const u = data.update;
  const pu = phpCheck ?? data.php_updates;
  const ups = pu.updates ?? [];
  const checkPHP = () => { setPhpBusy(true); post<PHPUpdates>('/system/php-updates/check').then(setPhpCheck).catch((e) => toast(e.message, true)).finally(() => setPhpBusy(false)); };
  const applyPHP = () => confirm(`Install ${ups.length} PHP package update${ups.length === 1 ? '' : 's'}? PHP-FPM restarts for a moment for each version.`) &&
    post<{ job_id: number }>('/system/php-updates/apply').then((r) => { setPhpCheck(null); setJob(r.job_id); }).catch((e) => toast(e.message, true));
  const byVersion = ups.reduce<Record<string, string>>((m, x) => { if (x.php && x.package === `php${x.php}-fpm`) m[x.php] = x.to.split('-')[0].split(':').pop()!; return m; }, {});
  const newer = u.latest && u.latest !== u.current;
  const update = () => {
    if (!confirm(`Update Berth to ${u.latest}? The panel restarts for a moment; sites keep running.`)) return;
    post('/update', { version: u.latest }).then(() => {
      setUpdating(true);
      const t = window.setInterval(() => get<{ version: string }>('/health').then((h) => { if (h.version === u.latest) { window.clearInterval(t); window.location.reload(); } }).catch(() => {}), 3000);
    }).catch((e) => toast(e.message, true));
  };
  return (
    <>
      <div className="head"><div><h1>System</h1><div className="sub">{data.name} · Berth {data.version}</div></div>
        <button className="btn" onClick={() => post('/system/apply').then(() => toast('Config re-applied')).catch((e) => toast(e.message, true))}><Icon name="sync" />Re-apply config</button></div>
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Berth version</h3><button className="btn s ghost" onClick={() => setCheck((c) => c + 1)}><Icon name="sync" size={12} />Check</button></div><div className="cb">
            <div className="kv" style={{ marginBottom: 14 }}>
              <div>Running</div><div className="mono">{u.current}</div>
              <div>Latest</div><div className="mono">{u.latest || '—'} {u.latest && (newer ? <Badge kind="w">Update available</Badge> : <Badge kind="g">Up to date</Badge>)}</div>
            </div>
            {u.check_error && <div className="warnbox">Could not check GitHub: {u.check_error}</div>}
            {updating ? <div className="note">Updating… this page reloads when Berth is back.</div> :
              <button className="btn p" disabled={!newer} onClick={update}>Update to {u.latest || 'latest'}</button>}
            <small className="help">Same as running <span className="mono">berth-update</span> in the container.</small>
          </div></div>
          <div className="card"><div className="ch"><h3>PHP versions</h3></div>
            <table><tbody>{data.php_versions.map((v) => {
              const inst = data.installed_php.includes(v);
              return (
                <tr key={v}><td><b>PHP {v}</b>{byVersion[v] && <> <Badge kind="w">{byVersion[v]} available</Badge></>}</td><td className="muted">{data.php_in_use[v] ? `${data.php_in_use[v]} site${data.php_in_use[v] === 1 ? '' : 's'}` : ''}</td>
                  <td style={{ textAlign: 'right' }}>{inst ? <Badge kind="g">Installed</Badge> :
                    <button className="btn s" onClick={() => confirm(`Install PHP ${v} from packages.sury.org?`) && post<{ job_id: number }>('/system/php/' + v).then((r) => setJob(r.job_id)).catch((e) => toast(e.message, true))}>Install</button>}</td></tr>
              );
            })}</tbody></table>
            <div className="cb dim" style={{ fontSize: 12, borderTop: '1px solid var(--line)' }}>Each site picks its own version on its Settings tab. Versions can run side by side.</div>
          </div>
          <div className="card"><div className="ch"><h3>PHP security updates</h3>
            <button className="btn s ghost" disabled={phpBusy || pu.checking} onClick={checkPHP}><Icon name="sync" size={12} />{phpBusy || pu.checking ? 'Checking…' : 'Check now'}</button></div>
            <div className="cb">
              {pu.error && <div className="warnbox">Could not check: {pu.error}</div>}
              {!pu.checked || pu.checked.startsWith('0001') ? <p className="muted" style={{ marginBottom: 12 }}>Not checked yet. Berth checks packages.sury.org once a day.</p> :
                ups.length === 0 ? <p style={{ marginBottom: 12 }}><Badge kind="g">Up to date</Badge> <span className="muted">Checked {ago(pu.checked)}.</span></p> :
                  <p style={{ marginBottom: 12 }}><Badge kind="w">{ups.length} update{ups.length === 1 ? '' : 's'}</Badge> <span className="muted">Security and bug-fix releases, checked {ago(pu.checked)}.</span></p>}
              {ups.length > 0 && <button className="btn p" onClick={applyPHP}>Install PHP updates</button>}
            </div>
            {ups.length > 0 && <table><tbody>{ups.map((x) => (
              <tr key={x.package}><td className="mono" style={{ fontSize: 12 }}>{x.package}</td><td className="mono muted" style={{ fontSize: 11 }}>{x.from.split('+')[0]} → {x.to.split('+')[0]}</td></tr>
            ))}</tbody></table>}
          </div>
        </div>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Services</h3></div><ServiceList services={data.services} /></div>
          <div className="card"><div className="ch"><h3>Disk</h3></div><div className="cb"><DiskBars disks={data.disks} /></div></div>
          <div className="card"><div className="ch"><h3>Paths</h3></div><div className="cb kv">
            <div>Sites</div><div className="mono">{data.paths.sites}</div>
            <div>nginx logs</div><div className="mono">{data.paths.logs}</div>
            <div>nginx config</div><div className="mono">{data.paths.nginx}</div>
            <div>Berth data</div><div className="mono">{data.paths.data}</div>
            <div>SFTP</div><div>{data.sftp ? 'Available' : 'Off'}</div>
          </div></div>
        </div>
      </div>
      {job !== null && <Modal title="PHP" onClose={() => { setJob(null); reload(); }}><JobView id={job} onDone={reload} /></Modal>}
    </>
  );
}
