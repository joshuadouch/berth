import { useState } from 'react';
import { Disk, get, post, Service } from '../api';
import { Badge, ErrorBox, Icon, JobView, Loading, Modal, useLoad, useToast } from '../ui';
import { DiskBars, ServiceList } from './Dashboard';

interface Sys {
  name: string; version: string; update: { current: string; latest: string; latest_url: string; check_error?: string };
  php_versions: string[]; installed_php: string[]; php_in_use: Record<string, number>; services: Service[]; disks: Disk[];
  paths: Record<string, string>; sftp: boolean;
}

export default function System() {
  const [check, setCheck] = useState(0);
  const { data, error, reload } = useLoad(() => get<Sys>('/system' + (check ? '?check=1' : '')), [check], 20000);
  const [job, setJob] = useState<number | null>(null);
  const [updating, setUpdating] = useState(false);
  const toast = useToast();
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const u = data.update;
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
                <tr key={v}><td><b>PHP {v}</b></td><td className="muted">{data.php_in_use[v] ? `${data.php_in_use[v]} site${data.php_in_use[v] === 1 ? '' : 's'}` : ''}</td>
                  <td style={{ textAlign: 'right' }}>{inst ? <Badge kind="g">Installed</Badge> :
                    <button className="btn s" onClick={() => confirm(`Install PHP ${v} from packages.sury.org?`) && post<{ job_id: number }>('/system/php/' + v).then((r) => setJob(r.job_id)).catch((e) => toast(e.message, true))}>Install</button>}</td></tr>
              );
            })}</tbody></table>
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
      {job !== null && <Modal title="Install PHP" onClose={() => { setJob(null); reload(); }}><JobView id={job} onDone={reload} /></Modal>}
    </>
  );
}
