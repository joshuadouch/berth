package engine

import (
	"bufio"
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

type trafficState struct{ mu sync.Mutex }

// logPos is how far into a site's access log Berth has read.
type logPos struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

// accessRe matches Berth's "berth" log format (see render.httpConf).
var accessRe = regexp.MustCompile(`^(\S+) - (\S+) \[([^\]]+)\] "((?:[^"\\]|\\.)*)" (\d{3}) (\d+) "((?:[^"\\]|\\.)*)" "((?:[^"\\]|\\.)*)"(?: ([\d.]+))?`)

// AccessLine is one parsed access log line.
type AccessLine struct {
	IP, Method, Path, Referrer, UA string
	Time                           time.Time
	Status                         int
	Bytes                          int64
	MS                             int64
}

// ParseAccessLine parses a line in the berth log format.
func ParseAccessLine(l string) (AccessLine, bool) {
	m := accessRe.FindStringSubmatch(l)
	if m == nil {
		return AccessLine{}, false
	}
	var a AccessLine
	t, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[3])
	if err != nil {
		return a, false
	}
	a.IP, a.Time = m[1], t
	parts := strings.SplitN(m[4], " ", 3)
	if len(parts) >= 2 {
		a.Method, a.Path = parts[0], parts[1]
	} else {
		a.Path = m[4]
	}
	if i := strings.IndexByte(a.Path, '?'); i >= 0 {
		a.Path = a.Path[:i]
	}
	if len(a.Path) > 200 {
		a.Path = a.Path[:200]
	}
	a.Status, _ = strconv.Atoi(m[5])
	a.Bytes, _ = strconv.ParseInt(m[6], 10, 64)
	a.Referrer, a.UA = m[7], m[8]
	if m[9] != "" {
		f, _ := strconv.ParseFloat(m[9], 64)
		a.MS = int64(f * 1000)
	}
	return a, true
}

var botRe = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|curl|wget|python|go-http|java/|headless|monitor|uptime|scan|fetch|http-client|okhttp|axios|feed`)

// browser names the visitor's browser family.
func browser(ua string) string {
	switch {
	case ua == "" || ua == "-":
		return "Unknown"
	case botRe.MatchString(ua):
		return "Bots and tools"
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		return "Opera"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/"):
		return "Safari"
	}
	return "Other"
}

var staticRe = regexp.MustCompile(`(?i)\.(?:css|js|mjs|jpe?g|png|gif|webp|avif|svg|ico|woff2?|ttf|otf|eot|mp4|webm|mp3|pdf|map|txt|xml)$`)

// slowMS is how long a request must take to be listed as slow.
const slowMS = 1000

// batchFor collects parsed lines into a store batch.
type trafficBatch struct {
	store.TrafficBatch
	own map[string]bool // the site's own hostnames, left out of referrers
}

func newTrafficBatch(s model.Site) *trafficBatch {
	b := &trafficBatch{TrafficBatch: store.TrafficBatch{Site: s.Name, Hours: map[int64]*store.TrafficHour{},
		Top: map[string]map[string]map[string]*store.TopEntry{}}, own: map[string]bool{}}
	for _, h := range s.Hostnames() {
		b.own[h] = true
	}
	return b
}

func (b *trafficBatch) top(day, kind, key string, a AccessLine) {
	k := b.Top[day]
	if k == nil {
		k = map[string]map[string]*store.TopEntry{}
		b.Top[day] = k
	}
	m := k[kind]
	if m == nil {
		m = map[string]*store.TopEntry{}
		k[kind] = m
	}
	e := m[key]
	if e == nil {
		e = &store.TopEntry{Key: key}
		m[key] = e
	}
	e.Requests++
	e.Bytes += a.Bytes
	e.MS += a.MS
}

func (b *trafficBatch) add(a AccessLine) {
	if strings.HasPrefix(a.UA, "Berth-") {
		return // Berth's own checks
	}
	h := a.Time.Unix() / 3600 * 3600
	t := b.Hours[h]
	if t == nil {
		t = &store.TrafficHour{Hour: h}
		b.Hours[h] = t
	}
	t.Requests++
	t.Bytes += a.Bytes
	t.MS += a.MS
	switch a.Status / 100 {
	case 2:
		t.S2++
	case 3:
		t.S3++
	case 4:
		t.S4++
	case 5:
		t.S5++
	}
	br := browser(a.UA)
	if br == "Bots and tools" {
		t.Bots++
	}
	day := a.Time.Local().Format("2006-01-02")
	if !staticRe.MatchString(a.Path) {
		b.top(day, "page", a.Path, a)
	}
	b.top(day, "ip", a.IP, a)
	b.top(day, "browser", br, a)
	if a.Status == 404 {
		b.top(day, "404", a.Path, a)
	}
	if a.Status >= 500 {
		b.top(day, "5xx", a.Path, a)
	}
	if a.Referrer != "" && a.Referrer != "-" {
		if u, err := url.Parse(a.Referrer); err == nil && u.Host != "" && !b.own[strings.ToLower(u.Hostname())] {
			b.top(day, "ref", strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), a)
		}
	}
	if a.MS >= slowMS {
		b.Slow = append(b.Slow, store.SlowRequest{Time: a.Time.Unix(), Method: a.Method, Path: a.Path, Status: a.Status, MS: a.MS})
	}
}

func (b *trafficBatch) empty() bool { return len(b.Hours) == 0 }

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// readLog feeds lines from offset to the end of path into b, returning the new offset.
func readLog(path string, offset int64, b *trafficBatch) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			// A partial last line is read again next time.
			return offset, nil
		}
		offset += int64(len(line))
		if a, ok := ParseAccessLine(strings.TrimRight(line, "\n")); ok {
			b.add(a)
		}
	}
}

// IngestTraffic reads new access log lines of one site into the stats tables.
func (e *Engine) IngestTraffic(ctx context.Context, s model.Site) error {
	e.traffic.mu.Lock()
	defer e.traffic.mu.Unlock()
	path := filepath.Join(e.Cfg.LogDir, s.Name, "access.log")
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	key := "traffic_pos_" + s.Name
	var pos logPos
	_ = e.Store.GetKV(ctx, key, &pos)
	b := newTrafficBatch(s)
	ino := inode(fi)
	if pos.Inode != 0 && pos.Inode != ino {
		// Rotated: finish the old file (now access.log.1) first.
		if old, err := os.Stat(path + ".1"); err == nil && inode(old) == pos.Inode {
			_, _ = readLog(path+".1", pos.Offset, b)
		}
		pos = logPos{Inode: ino}
	}
	if fi.Size() < pos.Offset {
		pos.Offset = 0 // truncated
	}
	pos.Inode = ino
	off, err := readLog(path, pos.Offset, b)
	if err != nil {
		return err
	}
	if !b.empty() {
		if err := e.Store.AddTraffic(ctx, b.TrafficBatch); err != nil {
			return err
		}
	}
	pos.Offset = off
	return e.Store.SetKV(ctx, key, pos)
}

// ingestAllTraffic reads every site's new log lines.
func (e *Engine) ingestAllTraffic(ctx context.Context) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	for _, s := range sites {
		if s.Status == model.StatusDeleting {
			continue
		}
		if err := e.IngestTraffic(ctx, s); err != nil {
			e.Log.Warn("traffic", "site", s.Name, "err", err)
		}
	}
}
