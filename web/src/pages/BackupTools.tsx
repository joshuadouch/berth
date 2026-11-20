import { useState } from 'react';
import { Backup, bytes, fmtDate, FileEntry, get, post, Site } from '../api';
import { Badge, ErrorBox, Icon, Loading, Modal, useLoad } from '../ui';

interface Listing { path: string; entries: FileEntry[]; has_db: boolean; db_size: number; files: number }

type StartJob = (p: Promise<{ job_id: number }>, m?: string) => void;

export function Where({ b }: { b: Backup }) {
  return <>{b.local && <Badge kind="n">Local</Badge>} {b.remote && <Badge kind="a">Off-site</Badge>}</>;
}

// RestoreModal restores a whole backup: everything, only files or only the database.
export function RestoreModal({ site, b, onClose, startJob }: { site: Site; b: Backup; onClose: () => void; startJob: StartJob }) {
  const [what, setWhat] = useState<'all' | 'files' | 'db'>('all');
  const opts: { v: typeof what; title: string; text: string }[] = [
    { v: 'all', title: 'Everything', text: site.db ? 'Files and database, as they were.' : 'All files, as they were.' },
    { v: 'files', title: 'Files only', text: 'Replaces the files and keeps the current database.' },
  ];
  if (site.db) opts.push({ v: 'db', title: 'Database only', text: 'Replaces the database and keeps the current files.' });
  return (
    <Modal title={`Restore ${site.domain}`} onClose={onClose}>
      <p className="muted" style={{ marginBottom: 12 }}>From <span className="mono">{b.file}</span>, taken {fmtDate(b.time)}{!b.local && ' (downloaded from off-site storage first)'}.</p>
      <div className="typecards" style={{ gridTemplateColumns: `repeat(${opts.length},1fr)`, marginBottom: 12 }}>
        {opts.map((o) => <button type="button" key={o.v} className={'typecard' + (what === o.v ? ' on' : '')} onClick={() => setWhat(o.v)}><b>{o.title}</b><span>{o.text}</span></button>)}
      </div>
      <div className="note">Berth backs up the site as it is now before restoring, so you can undo this.</div>
      <button className="btn p" onClick={() => { startJob(post(`/sites/${site.id}/backups/${b.file}/restore`, { what }), 'Restoring'); onClose(); }}><Icon name="hist" size={13} />Restore {what === 'all' ? 'everything' : what === 'files' ? 'files' : 'database'}</button>
    </Modal>
  );
}

// BackupBrowser shows the files inside a backup, downloads single files and
// restores chosen files or folders.
export function BackupBrowser({ site, b, onClose, startJob }: { site: Site; b: Backup; onClose: () => void; startJob: StartJob }) {
  const [dir, setDir] = useState('public');
  const list = useLoad(() => get<Listing>(`/sites/${site.id}/backups/${b.file}/browse?path=${encodeURIComponent(dir)}`), [dir]);
  const [sel, setSel] = useState<string[]>([]);
  const at = (n: string) => (dir === '.' ? n : dir + '/' + n);
  const parts = dir === '.' ? [] : dir.split('/');
  const cd = (d: string) => { setDir(d); };
  const dl = (p: string) => `/api/sites/${site.id}/backups/${b.file}/file?path=${encodeURIComponent(p)}`;
  const toggle = (p: string) => setSel(sel.includes(p) ? sel.filter((x) => x !== p) : [...sel, p]);
  const restore = () => {
    if (!confirm(`Put back ${sel.length} item${sel.length === 1 ? '' : 's'} from this backup? Files with the same names are overwritten; nothing else changes.`)) return;
    startJob(post(`/sites/${site.id}/backups/${b.file}/restore`, { paths: sel }), 'Restoring files');
    onClose();
  };
  return (
    <Modal wide title={`Browse ${b.file}`} onClose={onClose}>
      {list.error ? <ErrorBox error={list.error} /> : !list.data ? <><Loading /><p className="dim" style={{ textAlign: 'center', fontSize: 12 }}>{b.local ? 'Reading the backup…' : 'Downloading from off-site storage and reading it…'}</p></> : <>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
          <div className="crumbs">
            <a onClick={() => cd('.')}><Icon name="folder" size={13} /></a>
            {parts.map((p, i) => <span key={i}> / {i === parts.length - 1 ? <b>{p}</b> : <a onClick={() => cd(parts.slice(0, i + 1).join('/'))}>{p}</a>}</span>)}
          </div>
          <span className="dim" style={{ fontSize: 12, marginLeft: 'auto' }}>{list.data.files.toLocaleString()} files{list.data.has_db && <> · database {bytes(list.data.db_size)} <a href={dl('database.sql')} title="Download database.sql"><Icon name="down" size={12} /></a></>}</span>
        </div>
        <div style={{ maxHeight: '55vh', overflow: 'auto', border: '1px solid var(--line)', borderRadius: 8 }}>
          <table className="fm">
            <tbody>
              {dir !== '.' && <tr><td style={{ width: 28 }}></td><td colSpan={3}><div className="nm" onClick={() => cd(parts.length > 1 ? parts.slice(0, -1).join('/') : '.')}><Icon name="folder" size={15} /><span className="muted">..</span></div></td></tr>}
              {list.data.entries.map((f) => {
                const p = at(f.name);
                const top = dir === '.';
                return (
                  <tr key={f.name} className={sel.includes(p) ? 'sel' : ''}>
                    <td style={{ width: 28 }}>{!top && <input type="checkbox" checked={sel.includes(p)} onChange={() => toggle(p)} />}</td>
                    <td><div className="nm" onClick={() => (f.dir ? cd(p) : !f.link && (window.location.href = dl(p)))}><Icon name={f.dir ? 'folder' : f.link ? 'link' : 'file'} size={15} /><span>{f.name}</span></div></td>
                    <td className="muted hide-sm">{f.dir ? '' : bytes(f.size)}</td>
                    <td className="muted hide-sm">{f.mtime && !f.mtime.startsWith('0001') ? fmtDate(f.mtime) : ''}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {list.data.entries.length === 0 && <div className="empty">Empty folder.</div>}
        </div>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginTop: 12 }}>
          <button className="btn p" disabled={sel.length === 0} onClick={restore}><Icon name="hist" size={13} />Restore {sel.length || ''} selected</button>
          <span className="dim" style={{ fontSize: 12 }}>Tick files or folders to put them back, or click a file to download it.</span>
        </div>
      </>}
    </Modal>
  );
}
