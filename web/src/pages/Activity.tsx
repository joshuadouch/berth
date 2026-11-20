import { useSearchParams, Link } from 'react-router-dom';
import { ago, Event, fmtDate, get, Job } from '../api';
import { ErrorBox, JobBadge, JobView, Loading, Modal, useLoad } from '../ui';

export default function Activity() {
  const [sp, setSp] = useSearchParams();
  const jobs = useLoad(() => get<Job[]>('/jobs'), [], 5000);
  const events = useLoad(() => get<Event[]>('/events'), [], 15000);
  const open = sp.get('job');
  return (
    <>
      <div className="head"><div><h1>Activity</h1><div className="sub">Background jobs and everything that changed.</div></div></div>
      <ErrorBox error={jobs.error || events.error} />
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="card"><div className="ch"><h3>Jobs</h3></div>
          {!jobs.data ? <Loading /> : jobs.data.length === 0 ? <div className="empty">No jobs yet.</div> : <table>
            <thead><tr><th>Job</th><th>Site</th><th>Started</th><th>Status</th></tr></thead>
            <tbody>{jobs.data.map((j) => (
              <tr key={j.id} className="click" onClick={() => setSp({ job: String(j.id) })}>
                <td style={{ whiteSpace: 'normal' }}>{j.title}{j.error && <div className="dim" style={{ fontSize: 12 }}>{j.error.split('\n')[0]}</div>}</td>
                <td>{j.site ? <Link to={'/sites/' + j.site} onClick={(e) => e.stopPropagation()}>{j.site}</Link> : <span className="dim">—</span>}</td>
                <td className="muted" title={fmtDate(j.started)}>{ago(j.started)}</td><td><JobBadge status={j.status} /></td>
              </tr>
            ))}</tbody>
          </table>}
        </div>
        <div className="card"><div className="ch"><h3>Events</h3></div>
          {!events.data ? <Loading /> : events.data.length === 0 ? <div className="empty">Nothing yet.</div> : <table><tbody>{events.data.map((e) => (
            <tr key={e.id}>
              <td>{e.level === 'error' ? <span className="dot b" /> : e.level === 'warn' ? <span className="dot w" /> : <span className="dot g" />}</td>
              <td style={{ whiteSpace: 'normal' }}>{e.message}</td>
              <td className="muted" style={{ textAlign: 'right' }} title={fmtDate(e.time)}>{ago(e.time)}</td>
            </tr>
          ))}</tbody></table>}
        </div>
      </div>
      {open && <Modal title="Job" onClose={() => { setSp({}); jobs.reload(); }}><JobView id={+open} onDone={jobs.reload} /></Modal>}
    </>
  );
}
