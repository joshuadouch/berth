import { useEffect, useState } from 'react';
import { bytes, get, post, Site } from '../api';
import { ErrorBox, Field, Icon, Loading, Modal, useLoad, useToast } from '../ui';

interface Table { name: string; rows: number; size: number; engine: string }
interface Column { name: string; type: string; nullable: boolean; key: string; default: string | null; extra: string }
type Cell = string | null | { binary: number };
interface Rows { columns: Column[]; primary: string[]; rows: Cell[][]; total: number }
interface QueryResult { columns: string[] | null; rows: Cell[][]; truncated: boolean; affected: number; ms: number }

const show = (c: Cell) => (c === null ? 'NULL' : typeof c === 'object' ? `(binary, ${bytes(c.binary)})` : c);

export default function DatabaseTab({ s }: { s: Site }) {
  const tables = useLoad(() => get<Table[]>(`/sites/${s.id}/db/tables`), [s.id]);
  const [table, setTable] = useState<string | null>(null);
  const [mode, setMode] = useState<'browse' | 'sql'>('browse');
  const [filter, setFilter] = useState('');
  useEffect(() => { if (!table && tables.data?.length) setTable(tables.data.find((t) => t.name.endsWith('posts'))?.name ?? tables.data[0].name); }, [tables.data, table]);
  if (tables.error) return <ErrorBox error={tables.error} />;
  if (!tables.data) return <Loading />;
  const list = tables.data.filter((t) => t.name.toLowerCase().includes(filter.toLowerCase()));
  return (
    <div className="dbsplit" style={{ display: 'grid', gridTemplateColumns: '240px minmax(0,1fr)', gap: 16, alignItems: 'start' }}>
      <div className="card">
        <div className="ch"><h3>{s.db?.name}</h3><span className="dim" style={{ fontSize: 12 }}>{tables.data.length} tables</span></div>
        <div style={{ padding: '10px 12px 6px' }}><input className="input" placeholder="Filter tables" value={filter} onChange={(e) => setFilter(e.target.value)} /></div>
        <div className="tables">
          <div className={mode === 'sql' ? 'on' : ''} onClick={() => setMode('sql')}><span><Icon name="play" size={12} /> SQL query</span></div>
          {list.map((t) => <div key={t.name} className={mode === 'browse' && t.name === table ? 'on' : ''} onClick={() => { setTable(t.name); setMode('browse'); }}>
            <span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.name}</span><span className="dim">{t.rows}</span></div>)}
          {list.length === 0 && <div className="dim">No tables.</div>}
        </div>
      </div>
      {mode === 'sql' ? <SQLPanel s={s} onChanged={tables.reload} /> : table ? <TableView key={table} s={s} table={table} onChanged={tables.reload} /> : <div className="card"><div className="empty">This database has no tables yet.</div></div>}
    </div>
  );
}

function TableView({ s, table, onChanged }: { s: Site; table: string; onChanged: () => void }) {
  const [page, setPage] = useState(0);
  const [sort, setSort] = useState<{ col: string; desc: boolean } | null>(null);
  const [q, setQ] = useState('');
  const [search, setSearch] = useState('');
  const size = 50;
  const qs = `page=${page}&size=${size}&q=${encodeURIComponent(search)}` + (sort ? `&sort=${encodeURIComponent(sort.col)}&desc=${sort.desc ? 1 : 0}` : '');
  const data = useLoad(() => get<Rows>(`/sites/${s.id}/db/tables/${encodeURIComponent(table)}?${qs}`), [qs]);
  const [edit, setEdit] = useState<{ row: Cell[] | null } | null>(null);
  const d = data.data;
  const pages = d ? Math.max(1, Math.ceil(d.total / size)) : 1;
  return (
    <div className="card">
      <div className="ch" style={{ flexWrap: 'wrap', gap: 10 }}>
        <h3 className="mono">{table}</h3>
        <div className="toolbar">
          <form onSubmit={(e) => { e.preventDefault(); setPage(0); setSearch(q); }} style={{ display: 'flex', gap: 6 }}>
            <input className="input" style={{ width: 200 }} placeholder="Search all columns" value={q} onChange={(e) => setQ(e.target.value)} />
            <button className="btn s"><Icon name="search" size={13} /></button>
          </form>
          <button className="btn s p" onClick={() => setEdit({ row: null })}><Icon name="plus" size={13} />Add row</button>
          <button className="btn s ghost" onClick={data.reload} title="Refresh"><Icon name="sync" size={12} /></button>
        </div>
      </div>
      {data.error ? <div className="cb"><ErrorBox error={data.error} /></div> : !d ? <Loading /> : <>
        {d.primary.length === 0 && <div className="cb" style={{ paddingBottom: 0 }}><div className="warnbox">This table has no primary key, so rows can be added but not edited here. Use an SQL query instead.</div></div>}
        <div className="dbgrid">
          <table>
            <thead><tr>{d.columns.map((c) => <th key={c.name} title={c.type} onClick={() => setSort(sort?.col === c.name ? { col: c.name, desc: !sort.desc } : { col: c.name, desc: false })}>
              {c.key === 'PRI' && <Icon name="key" size={11} />} {c.name}{sort?.col === c.name ? (sort.desc ? ' ↓' : ' ↑') : ''}<div className="dim" style={{ fontWeight: 400, fontSize: 11 }}>{c.type}</div></th>)}</tr></thead>
            <tbody>{d.rows.map((r, i) => (
              <tr key={i} onClick={() => setEdit({ row: r })}>{r.map((c, j) => <td key={j} className={c === null ? 'null' : ''} title={typeof c === 'string' ? c.slice(0, 500) : undefined}>{show(c)}</td>)}</tr>
            ))}</tbody>
          </table>
          {d.rows.length === 0 && <div className="empty">{search ? 'No rows match.' : 'No rows.'}</div>}
        </div>
        <div className="cb" style={{ display: 'flex', alignItems: 'center', gap: 10, borderTop: '1px solid var(--line)' }}>
          <button className="btn s" disabled={page === 0} onClick={() => setPage(page - 1)}>Previous</button>
          <span className="muted" style={{ fontSize: 12 }}>Page {page + 1} of {pages} · {d.total.toLocaleString()} rows</span>
          <button className="btn s" disabled={page + 1 >= pages} onClick={() => setPage(page + 1)}>Next</button>
          <span className="dim" style={{ fontSize: 12, marginLeft: 'auto' }}>Click a row to edit it.</span>
        </div>
        {edit && <RowEditor s={s} table={table} cols={d.columns} primary={d.primary} row={edit.row} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); data.reload(); onChanged(); }} />}
      </>}
    </div>
  );
}

function RowEditor({ s, table, cols, primary, row, onClose, onSaved }: { s: Site; table: string; cols: Column[]; primary: string[]; row: Cell[] | null; onClose: () => void; onSaved: () => void }) {
  const isNew = row === null;
  const init: Record<string, string | null> = {};
  cols.forEach((c, i) => {
    const v = row ? row[i] : null;
    init[c.name] = typeof v === 'object' && v !== null ? null : row ? (v as string | null) : null;
  });
  const [vals, setVals] = useState(init);
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  const binary = (i: number) => !!row && typeof row[i] === 'object' && row[i] !== null;
  const key: Record<string, Cell> = {};
  if (row) primary.forEach((k) => { key[k] = row[cols.findIndex((c) => c.name === k)]; });
  const canEdit = isNew || primary.length > 0;
  const set = (n: string, v: string | null) => { setVals({ ...vals, [n]: v }); setTouched({ ...touched, [n]: true }); };
  const save = async () => {
    setErr(null);
    const values: Record<string, string | null> = {};
    cols.forEach((c) => { if (touched[c.name]) values[c.name] = vals[c.name]; });
    try {
      await post(`/sites/${s.id}/db/tables/${encodeURIComponent(table)}/${isNew ? 'insert' : 'update'}`, isNew ? { values } : { key, values });
      toast(isNew ? 'Row added' : 'Row saved'); onSaved();
    } catch (e) { setErr(e); }
  };
  const remove = async () => {
    if (!confirm('Delete this row?')) return;
    try { await post(`/sites/${s.id}/db/tables/${encodeURIComponent(table)}/delete`, { key }); toast('Row deleted'); onSaved(); } catch (e) { setErr(e); }
  };
  return (
    <Modal wide title={isNew ? `New row in ${table}` : `Edit row in ${table}`} onClose={onClose}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill,minmax(260px,1fr))', gap: '0 14px' }}>
        {cols.map((c, i) => {
          const v = vals[c.name];
          const long = (typeof v === 'string' && (v.length > 80 || v.includes('\n'))) || /text|blob|json/i.test(c.type);
          const auto = isNew && c.extra.includes('auto_increment');
          return (
            <Field key={c.name} label={`${c.name}${c.key === 'PRI' ? ' (key)' : ''}`} style={long ? { gridColumn: '1 / -1' } : undefined}
              help={<>{c.type}{c.nullable && <> · <a style={{ cursor: 'pointer' }} onClick={() => set(c.name, v === null ? '' : null)}>{v === null ? 'set a value' : 'set NULL'}</a></>}{auto && ' · leave empty to number it automatically'}</>}>
              {binary(i) ? <div className="muted">Binary data, not editable here</div> : long ?
                <textarea className="sqlbox" disabled={!canEdit} value={v ?? ''} placeholder={v === null ? 'NULL' : ''} onChange={(e) => set(c.name, e.target.value)} style={{ minHeight: 120 }} /> :
                <input className="input mono" disabled={!canEdit} value={v ?? ''} placeholder={v === null ? 'NULL' : ''} onChange={(e) => set(c.name, e.target.value)} />}
            </Field>
          );
        })}
      </div>
      <ErrorBox error={err} />
      <div style={{ display: 'flex', gap: 8 }}>
        {canEdit && <button className="btn p" disabled={!isNew && Object.keys(touched).length === 0} onClick={save}>{isNew ? 'Add row' : 'Save changes'}</button>}
        {!isNew && primary.length > 0 && <button className="btn" onClick={remove} style={{ marginLeft: 'auto' }}><Icon name="trash" size={13} />Delete row</button>}
      </div>
    </Modal>
  );
}

function SQLPanel({ s, onChanged }: { s: Site; onChanged: () => void }) {
  const [sql, setSql] = useState('SELECT * FROM ' + (s.type === 'wordpress' ? 'wp_options WHERE autoload = \'yes\' LIMIT 20' : 'information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()'));
  const [res, setRes] = useState<QueryResult | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const run = async () => {
    setErr(null); setBusy(true);
    try { const r = await post<QueryResult>(`/sites/${s.id}/db/sql`, { sql }); setRes(r); if (!r.columns?.length) onChanged(); } catch (e) { setErr(e); setRes(null); } finally { setBusy(false); }
  };
  return (
    <div className="card">
      <div className="ch"><h3>SQL query</h3><span className="dim" style={{ fontSize: 12 }}>Runs as {s.db?.user}, one statement at a time</span></div>
      <div className="cb">
        <textarea className="sqlbox" spellCheck={false} value={sql} onChange={(e) => setSql(e.target.value)} onKeyDown={(e) => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); run(); } }} />
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginTop: 10 }}>
          <button className="btn p" disabled={busy} onClick={run}><Icon name="play" size={12} />Run</button>
          <span className="dim" style={{ fontSize: 12 }}>Ctrl+Enter runs it. Changes are not undoable, so take a backup first if unsure.</span>
        </div>
        <div style={{ marginTop: 12 }}><ErrorBox error={err} /></div>
        {res && (res.columns?.length ? <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>{res.rows.length} row{res.rows.length === 1 ? '' : 's'}{res.truncated ? ' (first 1,000 shown)' : ''} · {res.ms} ms</div> :
          <div className="note">Done. {res.affected} row{res.affected === 1 ? '' : 's'} affected · {res.ms} ms</div>)}
      </div>
      {res?.columns?.length ? <div className="dbgrid"><table>
        <thead><tr>{res.columns.map((c) => <th key={c}>{c}</th>)}</tr></thead>
        <tbody>{res.rows.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j} className={c === null ? 'null' : ''} title={typeof c === 'string' ? c.slice(0, 500) : undefined}>{show(c)}</td>)}</tr>)}</tbody>
      </table></div> : null}
    </div>
  );
}
