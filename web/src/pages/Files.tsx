import { DragEvent, useRef, useState } from 'react';
import { bytes, fmtDate, FileEntry, get, post, Site, upload } from '../api';
import { ErrorBox, Field, Icon, Loading, Modal, useLoad, useToast } from '../ui';

const textExt = /\.(php|phtml|html?|css|js|mjs|json|txt|md|xml|svg|ini|conf|htaccess|env|ya?ml|log|sql|csv|twig|blade\.php|ts|tsx|jsx|scss|less|sh|py|rb|lock|map)$/i;
const isText = (name: string) => textExt.test(name) || name.startsWith('.') || !name.includes('.');

export default function FilesTab({ s }: { s: Site }) {
  const [dir, setDir] = useState('public');
  const list = useLoad(() => get<FileEntry[]>(`/sites/${s.id}/files?path=${encodeURIComponent(dir)}`), [s.id, dir]);
  const [sel, setSel] = useState<string[]>([]);
  const [edit, setEdit] = useState<{ path: string; content: string; orig: string } | null>(null);
  const [prompt, setPrompt] = useState<{ title: string; label: string; value: string; ok: (v: string) => Promise<unknown> } | null>(null);
  const [progress, setProgress] = useState<number | null>(null);
  const [drag, setDrag] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const toast = useToast();
  const top = dir === '.';
  const at = (n: string) => (dir === '.' ? n : dir + '/' + n);
  const cd = (d: string) => { setDir(d); setSel([]); setErr(null); };
  const act = (body: object, ok: string) => post(`/sites/${s.id}/files`, body).then(() => { toast(ok); list.reload(); setSel([]); }).catch((e) => setErr(e));
  const canWrite = dir !== '.';

  const send = async (files: { file: File; name: string }[]) => {
    if (!files.length) return;
    if (!canWrite) { setErr(new Error('Open a folder such as public to upload into it.')); return; }
    setErr(null); setProgress(0);
    try { await upload(`/sites/${s.id}/files/upload?path=${encodeURIComponent(dir)}`, files, setProgress); toast(`Uploaded ${files.length} file${files.length === 1 ? '' : 's'}`); list.reload(); }
    catch (e) { setErr(e); } finally { setProgress(null); }
  };
  const onDrop = async (e: DragEvent) => {
    e.preventDefault(); setDrag(false);
    const out: { file: File; name: string }[] = [];
    const walk = async (entry: any, prefix: string): Promise<void> => {
      if (entry.isFile) { await new Promise<void>((res) => entry.file((f: File) => { out.push({ file: f, name: prefix + f.name }); res(); }, () => res())); return; }
      if (entry.isDirectory) {
        const reader = entry.createReader();
        for (;;) {
          const batch: any[] = await new Promise((res) => reader.readEntries(res, () => res([])));
          if (!batch.length) break;
          for (const c of batch) await walk(c, prefix + entry.name + '/');
        }
      }
    };
    const items = Array.from(e.dataTransfer.items);
    if (items.length && (items[0] as any).webkitGetAsEntry) {
      for (const it of items) { const en = (it as any).webkitGetAsEntry(); if (en) await walk(en, ''); }
    } else Array.from(e.dataTransfer.files).forEach((f) => out.push({ file: f, name: f.name }));
    send(out);
  };
  const open = async (f: FileEntry) => {
    if (f.dir) return cd(at(f.name));
    if (!isText(f.name) || f.size > 2 << 20) { window.location.href = `/api/sites/${s.id}/files/download?path=${encodeURIComponent(at(f.name))}`; return; }
    try { const r = await get<{ content: string }>(`/sites/${s.id}/files/content?path=${encodeURIComponent(at(f.name))}`); setEdit({ path: at(f.name), content: r.content, orig: r.content }); }
    catch (e) { setErr(e); }
  };
  const save = () => edit && post(`/sites/${s.id}/files`, { action: 'save', path: edit.path, content: edit.content })
    .then(() => { toast('Saved'); setEdit({ ...edit, orig: edit.content }); list.reload(); }).catch((e) => toast(e.message, true));

  const parts = dir === '.' ? [] : dir.split('/');
  const all = list.data ?? [];
  const toggle = (n: string) => setSel(sel.includes(n) ? sel.filter((x) => x !== n) : [...sel, n]);

  return (
    <div className={'card' + (drag ? ' dropzone' : '')} onDragOver={(e) => { e.preventDefault(); setDrag(true); }} onDragLeave={() => setDrag(false)} onDrop={onDrop}>
      <div className="ch" style={{ gap: 10, flexWrap: 'wrap' }}>
        <div className="crumbs">
          <a onClick={() => cd('.')}><Icon name="folder" size={13} /></a>
          {parts.map((p, i) => <span key={i}> / {i === parts.length - 1 ? <b>{p}</b> : <a onClick={() => cd(parts.slice(0, i + 1).join('/'))}>{p}</a>}</span>)}
        </div>
        <div className="toolbar">
          {sel.length > 0 && canWrite && <button className="btn s" onClick={() => confirm(`Delete ${sel.length} item${sel.length === 1 ? '' : 's'}? This cannot be undone (backups can bring files back).`) && act({ action: 'delete', paths: sel.map(at) }, 'Deleted')}><Icon name="trash" size={13} />Delete {sel.length}</button>}
          {canWrite && <>
            <button className="btn s" onClick={() => setPrompt({ title: 'New folder', label: 'Folder name', value: '', ok: (v) => act({ action: 'mkdir', path: at(v) }, 'Folder created') })}><Icon name="folder" size={13} />New folder</button>
            <button className="btn s" onClick={() => setPrompt({ title: 'New file', label: 'File name', value: '', ok: async (v) => { await post(`/sites/${s.id}/files`, { action: 'save', path: at(v), content: '' }); list.reload(); setEdit({ path: at(v), content: '', orig: '' }); } })}><Icon name="file" size={13} />New file</button>
            <button className="btn s p" onClick={() => fileInput.current?.click()}><Icon name="up" size={13} />Upload</button>
          </>}
          <button className="btn s ghost" onClick={list.reload} title="Refresh"><Icon name="sync" size={12} /></button>
          <input ref={fileInput} type="file" multiple hidden onChange={(e) => { send(Array.from(e.target.files ?? []).map((f) => ({ file: f, name: f.name }))); e.target.value = ''; }} />
        </div>
      </div>
      {progress !== null && <div className="cb" style={{ paddingBottom: 0 }}><div className="bar"><i style={{ width: progress + '%', background: 'var(--accent)' }} /></div><small className="help">Uploading… {progress}%</small></div>}
      {err ? <div className="cb" style={{ paddingBottom: 0 }}><ErrorBox error={err} /></div> : null}
      {!list.data ? (list.error ? <div className="cb"><ErrorBox error={list.error} /></div> : <Loading />) : all.length === 0 ? <div className="empty">This folder is empty. Drop files here to upload them.</div> :
        <table className="fm">
          <thead><tr><th style={{ width: 28 }}>{canWrite && <input type="checkbox" checked={sel.length === all.length} onChange={() => setSel(sel.length === all.length ? [] : all.map((f) => f.name))} />}</th><th>Name</th><th className="hide-sm">Size</th><th className="hide-sm">Modified</th><th className="hide-sm">Mode</th><th></th></tr></thead>
          <tbody>
            {dir !== '.' && <tr><td></td><td colSpan={5}><div className="nm" onClick={() => cd(parts.length > 1 ? parts.slice(0, -1).join('/') : '.')}><Icon name="folder" size={15} /><span className="muted">..</span></div></td></tr>}
            {all.map((f) => (
              <tr key={f.name} className={sel.includes(f.name) ? 'sel' : ''}>
                <td>{canWrite && <input type="checkbox" checked={sel.includes(f.name)} onChange={() => toggle(f.name)} />}</td>
                <td><div className="nm" onClick={() => open(f)} title={f.link ? '→ ' + f.link : f.name}>
                  <Icon name={f.dir ? 'folder' : f.name.endsWith('.zip') ? 'zip' : f.link ? 'link' : 'file'} size={15} /><span>{f.name}</span></div></td>
                <td className="muted hide-sm">{f.dir ? '' : bytes(f.size)}</td>
                <td className="muted hide-sm">{fmtDate(f.mtime)}</td>
                <td className="mono muted hide-sm" style={{ fontSize: 12 }}>{f.mode.slice(-3)}</td>
                <td className="actions">
                  {!f.dir && <a className="btn s ghost" title="Download" href={`/api/sites/${s.id}/files/download?path=${encodeURIComponent(at(f.name))}`}><Icon name="down" size={13} /></a>}
                  {canWrite && f.name.endsWith('.zip') && <button className="btn s ghost" title="Extract here" onClick={() => post<{ files: number }>(`/sites/${s.id}/files`, { action: 'unzip', path: at(f.name) }).then((r) => { toast(`Extracted ${r.files} files`); list.reload(); }).catch((e) => setErr(e))}><Icon name="zip" size={13} /></button>}
                  {canWrite && <button className="btn s ghost" title="Rename or move" onClick={() => setPrompt({ title: 'Rename or move', label: 'New path inside the site', value: at(f.name), ok: (v) => act({ action: 'rename', path: at(f.name), to: v }, 'Renamed') })}><Icon name="edit" size={13} /></button>}
                  {canWrite && <button className="btn s ghost" title="Permissions" onClick={() => setPrompt({ title: 'Permissions for ' + f.name, label: 'Mode (for example 644 or 755)', value: f.mode.slice(-3), ok: (v) => act({ action: 'chmod', path: at(f.name), mode: v }, 'Permissions changed') })}><Icon name="key" size={13} /></button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>}
      <div className="cb dim" style={{ fontSize: 12, borderTop: '1px solid var(--line)' }}>
        {top ? 'The site folder. Open public for the web root.' : 'Drag files or folders here to upload. New files belong to ' + s.name + "'s user, like files uploaded over SFTP."}
      </div>
      {edit && <Modal wide title={edit.path} onClose={() => (edit.content === edit.orig || confirm('Discard unsaved changes?')) && setEdit(null)}>
        <textarea className="editor" spellCheck={false} value={edit.content} onChange={(e) => setEdit({ ...edit, content: e.target.value })}
          onKeyDown={(e) => {
            if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); save(); }
            if (e.key === 'Tab') { e.preventDefault(); const t = e.currentTarget; const a = t.selectionStart; const v = edit.content.slice(0, a) + '\t' + edit.content.slice(t.selectionEnd); setEdit({ ...edit, content: v }); requestAnimationFrame(() => { t.selectionStart = t.selectionEnd = a + 1; }); }
          }} />
        <div style={{ display: 'flex', gap: 8, marginTop: 12, alignItems: 'center' }}>
          <button className="btn p" disabled={edit.content === edit.orig} onClick={save}>Save</button>
          <span className="dim" style={{ fontSize: 12 }}>{edit.content === edit.orig ? 'No changes' : 'Unsaved changes'} · Ctrl+S saves</span>
        </div>
      </Modal>}
      {prompt && <PromptModal {...prompt} onClose={() => setPrompt(null)} />}
    </div>
  );
}

function PromptModal({ title, label, value, ok, onClose }: { title: string; label: string; value: string; ok: (v: string) => Promise<unknown>; onClose: () => void }) {
  const [v, setV] = useState(value);
  const [err, setErr] = useState<unknown>(null);
  return (
    <Modal title={title} onClose={onClose}>
      <form onSubmit={(e) => { e.preventDefault(); ok(v.trim()).then(onClose).catch(setErr); }}>
        <Field label={label}><input className="input mono" autoFocus value={v} onChange={(e) => setV(e.target.value)} /></Field>
        <ErrorBox error={err} />
        <div style={{ marginTop: 12 }}><button className="btn p" disabled={!v.trim()}>OK</button></div>
      </form>
    </Modal>
  );
}
