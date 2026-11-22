package store

import (
	"context"
	"database/sql"
	"time"
)

// Monitoring, analytics and resource history. Times are Unix seconds.
const statsSchema = `
CREATE TABLE IF NOT EXISTS checks (
  site TEXT NOT NULL, time INTEGER NOT NULL, ok INTEGER NOT NULL, status INTEGER NOT NULL,
  ms INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS checks_site_time ON checks(site, time);
CREATE TABLE IF NOT EXISTS traffic (
  site TEXT NOT NULL, hour INTEGER NOT NULL, requests INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0,
  s2 INTEGER NOT NULL DEFAULT 0, s3 INTEGER NOT NULL DEFAULT 0, s4 INTEGER NOT NULL DEFAULT 0, s5 INTEGER NOT NULL DEFAULT 0,
  bots INTEGER NOT NULL DEFAULT 0, ms INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (site, hour));
CREATE TABLE IF NOT EXISTS traffic_top (
  site TEXT NOT NULL, day TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
  requests INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0, ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (site, day, kind, key));
CREATE TABLE IF NOT EXISTS slow_requests (
  site TEXT NOT NULL, time INTEGER NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, status INTEGER NOT NULL, ms INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS slow_site_time ON slow_requests(site, time);
CREATE TABLE IF NOT EXISTS metrics (
  site TEXT NOT NULL, time INTEGER NOT NULL, cpu REAL NOT NULL DEFAULT 0, mem INTEGER NOT NULL DEFAULT 0,
  disk INTEGER NOT NULL DEFAULT 0, db INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (site, time));
`

// Check is one uptime check.
type Check struct {
	Site   string `json:"-"`
	Time   int64  `json:"time"`
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	MS     int    `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// AddCheck records an uptime check.
func (s *Store) AddCheck(ctx context.Context, c Check) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO checks(site,time,ok,status,ms,error) VALUES(?,?,?,?,?,?)`,
		c.Site, c.Time, boolInt(c.OK), c.Status, c.MS, c.Error)
	return err
}

// Checks returns a site's checks since a time, oldest first.
func (s *Store) Checks(ctx context.Context, site string, since int64) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT time,ok,status,ms,error FROM checks WHERE site=? AND time>=? ORDER BY time`, site, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		c := Check{Site: site}
		var ok int
		if err := rows.Scan(&c.Time, &ok, &c.Status, &c.MS, &c.Error); err != nil {
			return nil, err
		}
		c.OK = ok == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// UptimeSummary is the share of good checks over a period.
type UptimeSummary struct {
	Checks int     `json:"checks"`
	Up     int     `json:"up"`
	AvgMS  float64 `json:"avg_ms"`
}

// UptimeSince summarises every site's checks since a time.
func (s *Store) UptimeSince(ctx context.Context, since int64) (map[string]UptimeSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT site, COUNT(*), SUM(ok), AVG(CASE WHEN ok=1 THEN ms END) FROM checks WHERE time>=? GROUP BY site`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]UptimeSummary{}
	for rows.Next() {
		var site string
		var u UptimeSummary
		var avg sql.NullFloat64
		if err := rows.Scan(&site, &u.Checks, &u.Up, &avg); err != nil {
			return nil, err
		}
		u.AvgMS = avg.Float64
		out[site] = u
	}
	return out, rows.Err()
}

// TrafficHour is one hour of a site's requests.
type TrafficHour struct {
	Hour     int64 `json:"hour"`
	Requests int64 `json:"requests"`
	Bytes    int64 `json:"bytes"`
	S2       int64 `json:"s2"`
	S3       int64 `json:"s3"`
	S4       int64 `json:"s4"`
	S5       int64 `json:"s5"`
	Bots     int64 `json:"bots"`
	MS       int64 `json:"ms"` // total request time
}

// TopEntry is one row of a top list (pages, IPs, referrers...).
type TopEntry struct {
	Key      string `json:"key"`
	Requests int64  `json:"requests"`
	Bytes    int64  `json:"bytes"`
	MS       int64  `json:"ms"`
}

// SlowRequest is a request that took a second or more.
type SlowRequest struct {
	Time   int64  `json:"time"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	MS     int64  `json:"ms"`
}

// TrafficBatch is parsed log data to add in one transaction.
type TrafficBatch struct {
	Site  string
	Hours map[int64]*TrafficHour
	// Top maps day -> kind -> key -> totals.
	Top  map[string]map[string]map[string]*TopEntry
	Slow []SlowRequest
}

// AddTraffic adds a batch of parsed log lines.
func (s *Store) AddTraffic(ctx context.Context, b TrafficBatch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for h, t := range b.Hours {
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic(site,hour,requests,bytes,s2,s3,s4,s5,bots,ms) VALUES(?,?,?,?,?,?,?,?,?,?)
		  ON CONFLICT(site,hour) DO UPDATE SET requests=requests+excluded.requests, bytes=bytes+excluded.bytes,
		  s2=s2+excluded.s2, s3=s3+excluded.s3, s4=s4+excluded.s4, s5=s5+excluded.s5, bots=bots+excluded.bots, ms=ms+excluded.ms`,
			b.Site, h, t.Requests, t.Bytes, t.S2, t.S3, t.S4, t.S5, t.Bots, t.MS); err != nil {
			return err
		}
	}
	for day, kinds := range b.Top {
		for kind, keys := range kinds {
			for key, e := range keys {
				if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_top(site,day,kind,key,requests,bytes,ms) VALUES(?,?,?,?,?,?,?)
				  ON CONFLICT(site,day,kind,key) DO UPDATE SET requests=requests+excluded.requests, bytes=bytes+excluded.bytes, ms=ms+excluded.ms`,
					b.Site, day, kind, key, e.Requests, e.Bytes, e.MS); err != nil {
					return err
				}
			}
		}
	}
	for _, r := range b.Slow {
		if _, err := tx.ExecContext(ctx, `INSERT INTO slow_requests(site,time,method,path,status,ms) VALUES(?,?,?,?,?,?)`,
			b.Site, r.Time, r.Method, r.Path, r.Status, r.MS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Traffic returns a site's hourly traffic since a time ("" site = all sites).
func (s *Store) Traffic(ctx context.Context, site string, since int64) ([]TrafficHour, error) {
	q := `SELECT hour,SUM(requests),SUM(bytes),SUM(s2),SUM(s3),SUM(s4),SUM(s5),SUM(bots),SUM(ms) FROM traffic WHERE hour>=?`
	args := []any{since}
	if site != "" {
		q += ` AND site=?`
		args = append(args, site)
	}
	rows, err := s.db.QueryContext(ctx, q+` GROUP BY hour ORDER BY hour`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrafficHour{}
	for rows.Next() {
		var t TrafficHour
		if err := rows.Scan(&t.Hour, &t.Requests, &t.Bytes, &t.S2, &t.S3, &t.S4, &t.S5, &t.Bots, &t.MS); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Top returns the biggest keys of a kind since a day (YYYY-MM-DD).
func (s *Store) Top(ctx context.Context, site, kind, sinceDay, order string, limit int) ([]TopEntry, error) {
	col := "SUM(requests)"
	switch order {
	case "bytes":
		col = "SUM(bytes)"
	case "ms":
		col = "SUM(ms)*1.0/SUM(requests)"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key,SUM(requests),SUM(bytes),SUM(ms) FROM traffic_top WHERE site=? AND kind=? AND day>=?
	  GROUP BY key ORDER BY `+col+` DESC LIMIT ?`, site, kind, sinceDay, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TopEntry{}
	for rows.Next() {
		var e TopEntry
		if err := rows.Scan(&e.Key, &e.Requests, &e.Bytes, &e.MS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DailyUnique counts distinct keys of a kind per day (unique visitors by IP).
func (s *Store) DailyUnique(ctx context.Context, site, kind, sinceDay string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day, COUNT(*) FROM traffic_top WHERE site=? AND kind=? AND day>=? GROUP BY day`, site, kind, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var d string
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d] = n
	}
	return out, rows.Err()
}

// SlowRequests returns a site's slowest requests since a time.
func (s *Store) SlowRequests(ctx context.Context, site string, since int64, limit int) ([]SlowRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT time,method,path,status,ms FROM slow_requests WHERE site=? AND time>=? ORDER BY ms DESC LIMIT ?`, site, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SlowRequest{}
	for rows.Next() {
		var r SlowRequest
		if err := rows.Scan(&r.Time, &r.Method, &r.Path, &r.Status, &r.MS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Metric is a resource sample for a site ("" is the whole server).
type Metric struct {
	Time int64   `json:"time"`
	CPU  float64 `json:"cpu"` // percent of one core
	Mem  int64   `json:"mem"` // bytes
	Disk int64   `json:"disk"`
	DB   int64   `json:"db"`
}

// AddMetrics stores samples for several sites at once.
func (s *Store) AddMetrics(ctx context.Context, at int64, m map[string]Metric) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for site, v := range m {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metrics(site,time,cpu,mem,disk,db) VALUES(?,?,?,?,?,?)
		  ON CONFLICT(site,time) DO UPDATE SET cpu=excluded.cpu, mem=excluded.mem, disk=excluded.disk, db=excluded.db`,
			site, at, v.CPU, v.Mem, v.Disk, v.DB); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Metrics returns a site's samples since a time, averaged into buckets of step seconds.
func (s *Store) Metrics(ctx context.Context, site string, since, step int64) ([]Metric, error) {
	if step < 60 {
		step = 60
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (time/?)*?, AVG(cpu), CAST(AVG(mem) AS INTEGER), MAX(disk), MAX(db) FROM metrics
	  WHERE site=? AND time>=? GROUP BY time/? ORDER BY 1`, step, step, site, since, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metric{}
	for rows.Next() {
		var m Metric
		if err := rows.Scan(&m.Time, &m.CPU, &m.Mem, &m.Disk, &m.DB); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneStats deletes history older than the retention periods.
func (s *Store) PruneStats(ctx context.Context, now time.Time) {
	day := func(d int) int64 { return now.Add(-time.Duration(d) * 24 * time.Hour).Unix() }
	_, _ = s.db.ExecContext(ctx, `DELETE FROM checks WHERE time<?`, day(90))
	_, _ = s.db.ExecContext(ctx, `DELETE FROM traffic WHERE hour<?`, day(90))
	_, _ = s.db.ExecContext(ctx, `DELETE FROM traffic_top WHERE day<?`, now.AddDate(0, 0, -30).Format("2006-01-02"))
	_, _ = s.db.ExecContext(ctx, `DELETE FROM slow_requests WHERE time<?`, day(14))
	_, _ = s.db.ExecContext(ctx, `DELETE FROM metrics WHERE time<?`, day(30))
}

// DeleteSiteStats removes a deleted site's history.
func (s *Store) DeleteSiteStats(ctx context.Context, site string) {
	for _, t := range []string{"checks", "traffic", "traffic_top", "slow_requests", "metrics"} {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM `+t+` WHERE site=?`, site)
	}
}
