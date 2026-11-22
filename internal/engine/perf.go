package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joshuadouch/berth/internal/model"
)

// MariaDB's slow query log. Debian's logrotate already rotates this name.
const mysqlSlowLog = "/var/log/mysql/mariadb-slow.log"

// readTail returns up to max bytes from the end of a file, starting at a line.
func readTail(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if st.Size() > max {
		start = st.Size() - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	s := string(b)
	if start > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s, nil
}

// PHPSlowEntry is one stack trace from PHP-FPM's slow log.
type PHPSlowEntry struct {
	Time    string   `json:"time"`
	Script  string   `json:"script"`
	Culprit string   `json:"culprit"` // plugin or theme in the trace, if any
	Frames  []string `json:"frames"`
}

// Culprit is how often a plugin or theme appears in slow traces.
type Culprit struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var (
	slowHeadRe  = regexp.MustCompile(`^\[([^\]]+)\]\s+\[pool [^\]]+\] pid \d+`)
	slowFrameRe = regexp.MustCompile(`^\[0x[0-9a-f]+\] (.*)$`)
	wpPartRe    = regexp.MustCompile(`/wp-content/(plugins|themes|mu-plugins)/([^/]+)`)
)

// ParsePHPSlowLog parses PHP-FPM slow log text, newest entry first.
func ParsePHPSlowLog(text, siteDir string) []PHPSlowEntry {
	var out []PHPSlowEntry
	var cur *PHPSlowEntry
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if m := slowHeadRe.FindStringSubmatch(l); m != nil {
			out = append(out, PHPSlowEntry{Time: m[1]})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(l, "script_filename = ") {
			cur.Script = strings.TrimPrefix(strings.TrimPrefix(l, "script_filename = "), siteDir)
			continue
		}
		if m := slowFrameRe.FindStringSubmatch(l); m != nil && len(cur.Frames) < 40 {
			f := strings.ReplaceAll(m[1], siteDir+"/public", "")
			cur.Frames = append(cur.Frames, f)
			if cur.Culprit == "" {
				if w := wpPartRe.FindStringSubmatch(m[1]); w != nil {
					kind := strings.TrimSuffix(w[1], "s")
					if kind == "mu-plugin" {
						kind = "must-use plugin"
					}
					cur.Culprit = kind + " " + w[2]
				}
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// DBSlowQuery groups slow queries that differ only in their values.
type DBSlowQuery struct {
	Query    string  `json:"query"` // an example
	Count    int     `json:"count"`
	TotalS   float64 `json:"total_s"`
	MaxS     float64 `json:"max_s"`
	Examined int64   `json:"rows_examined"` // most rows examined by one run
	Last     int64   `json:"last"`          // unix time
}

var (
	slowUserRe  = regexp.MustCompile(`^# User@Host: (\S+?)\[`)
	slowStatsRe = regexp.MustCompile(`Query_time: ([\d.]+).*Rows_examined: (\d+)`)
	slowTSRe    = regexp.MustCompile(`^SET timestamp=(\d+);`)
	sqlStrRe    = regexp.MustCompile(`'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"`)
	sqlNumRe    = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
	sqlListRe   = regexp.MustCompile(`\((\s*\?\s*,)+\s*\?\s*\)`)
	spaceRe     = regexp.MustCompile(`\s+`)
)

func fingerprint(q string) string {
	q = sqlStrRe.ReplaceAllString(q, "?")
	q = sqlNumRe.ReplaceAllString(q, "?")
	q = sqlListRe.ReplaceAllString(q, "(?…)")
	return strings.ToLower(spaceRe.ReplaceAllString(strings.TrimSpace(q), " "))
}

// ParseMySQLSlowLog groups one database user's slow queries, slowest total first.
func ParseMySQLSlowLog(text, user string) []DBSlowQuery {
	groups := map[string]*DBSlowQuery{}
	type entry struct {
		user     string
		qt       float64
		examined int64
		ts       int64
		sql      []string
	}
	var cur *entry
	flush := func() {
		if cur == nil || cur.user != user || len(cur.sql) == 0 {
			return
		}
		q := strings.TrimSpace(strings.Join(cur.sql, "\n"))
		if q == "" {
			return
		}
		fp := fingerprint(q)
		g := groups[fp]
		if g == nil {
			if len(q) > 2000 {
				q = q[:2000] + "…"
			}
			g = &DBSlowQuery{Query: q}
			groups[fp] = g
		}
		g.Count++
		g.TotalS += cur.qt
		if cur.qt > g.MaxS {
			g.MaxS = cur.qt
		}
		if cur.examined > g.Examined {
			g.Examined = cur.examined
		}
		if cur.ts > g.Last {
			g.Last = cur.ts
		}
	}
	for _, l := range strings.Split(text, "\n") {
		if m := slowUserRe.FindStringSubmatch(l); m != nil {
			flush()
			cur = &entry{user: m[1]}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(l, "#") {
			if m := slowStatsRe.FindStringSubmatch(l); m != nil {
				cur.qt, _ = strconv.ParseFloat(m[1], 64)
				cur.examined, _ = strconv.ParseInt(m[2], 10, 64)
			}
			continue
		}
		if m := slowTSRe.FindStringSubmatch(l); m != nil {
			cur.ts, _ = strconv.ParseInt(m[1], 10, 64)
			continue
		}
		if strings.HasPrefix(strings.ToLower(l), "use ") && len(cur.sql) == 0 {
			continue
		}
		cur.sql = append(cur.sql, l)
	}
	flush()
	out := make([]DBSlowQuery, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalS > out[j].TotalS })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// Performance is what the Performance view shows.
type Performance struct {
	PHPSlow    []PHPSlowEntry `json:"php_slow"`
	Culprits   []Culprit      `json:"culprits"`
	DBSlow     []DBSlowQuery  `json:"db_slow"`
	DBSlowOn   bool           `json:"db_slow_on"`
	PHPSlowLog string         `json:"php_slow_log"`
}

// SitePerformance reads the site's PHP and database slow logs.
func (e *Engine) SitePerformance(ctx context.Context, s model.Site) Performance {
	dir := e.Paths().SiteDir(s.Name)
	p := Performance{PHPSlow: []PHPSlowEntry{}, Culprits: []Culprit{}, DBSlow: []DBSlowQuery{}, PHPSlowLog: filepath.Join(dir, "logs", "php-slow.log")}
	if text, err := readTail(p.PHPSlowLog, 1<<20); err == nil {
		all := ParsePHPSlowLog(text, dir)
		counts := map[string]int{}
		for _, en := range all {
			if en.Culprit != "" {
				counts[en.Culprit]++
			}
		}
		for k, v := range counts {
			p.Culprits = append(p.Culprits, Culprit{k, v})
		}
		sort.Slice(p.Culprits, func(i, j int) bool { return p.Culprits[i].Count > p.Culprits[j].Count })
		if len(all) > 50 {
			all = all[:50]
		}
		p.PHPSlow = all
	}
	if s.DB != nil {
		if text, err := readTail(mysqlSlowLog, 8<<20); err == nil {
			p.DBSlowOn = true
			p.DBSlow = ParseMySQLSlowLog(text, s.DB.User)
		} else {
			p.DBSlowOn = e.Cfg.DryRun
		}
	}
	return p
}

// ensureSlowQueryLog turns on MariaDB's slow query log (one second and up).
// It is set at runtime, so it is checked again every hour in case MariaDB restarted.
func (e *Engine) ensureSlowQueryLog(ctx context.Context) {
	if e.Cfg.DryRun {
		return
	}
	out, err := e.sql(ctx, "SELECT @@GLOBAL.slow_query_log, @@GLOBAL.slow_query_log_file")
	if err == nil && strings.Contains(out, "1") && strings.Contains(out, mysqlSlowLog) {
		return
	}
	if _, err := e.sql(ctx, "SET GLOBAL slow_query_log_file='"+mysqlSlowLog+"'; SET GLOBAL long_query_time=1; SET GLOBAL slow_query_log=1"); err != nil {
		e.Log.Warn("could not turn on the MariaDB slow query log", "err", err)
	}
}
