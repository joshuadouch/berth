import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Backup, bytes, get, post } from '../api';
import { ErrorBox, Icon, JobView, Loading, Modal, useLoad, useToast } from '../ui';
import { BackupTable } from './Site';

export default function Backups() {
  const { data, error, reload } = useLoad(() => get<{ backups: Backup[]; dir: string; keep: number; enabled: boolean; time: string }>('/backups'), [], 15000);
  const [job, setJob] = useState<number | null>(null);
  const toast = useToast();
  const startJob = (p: Promise<{ job_id: number }>, m?: string) => p.then((r) => { setJob(r.job_id); if (m) toast(m); }).catch((e) => toast(e.message, true));
  if (!data) return error ? <ErrorBox error={error} /> : <Loading />;
  const total = data.backups.reduce((n, b) => n + b.size, 0);
  return (
    <>
      <div className="head">
        <div><h1>Backups</h1><div className="sub">
          {data.enabled ? <>Every site with backups on is saved nightly at {data.time}, keeping the newest {data.keep}.</> : <>Nightly backups are off.</>} Stored in <span className="mono">{data.dir}</span> ({bytes(total)}). <Link to="/settings">Change</Link>
        </div></div>
        <button className="btn p" onClick={() => startJob(post('/backups/run'), 'Backing up every site')}><Icon name="backup" />Back up all sites now</button>
      </div>
      <div className="card"><BackupTable backups={data.backups} reload={reload} startJob={startJob} showSite /></div>
      <p className="dim" style={{ marginTop: 12, fontSize: 12 }}>To restore, open the site and use its Backups tab. To bring in a backup from elsewhere, copy the file into <span className="mono">{data.dir}/&lt;site&gt;/</span>.</p>
      {job !== null && <Modal title="Backup" onClose={() => { setJob(null); reload(); }}><JobView id={job} onDone={reload} /></Modal>}
    </>
  );
}
