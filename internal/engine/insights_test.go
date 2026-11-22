package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/render"
	"github.com/joshuadouch/berth/internal/store"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.10", "1.9", 1}, {"5.3.1", "5.3.1", 0}, {"5.3", "5.3.0", 0}, {"6.4.2", "6.5", -1},
		{"1.0-beta", "1.0", -1}, {"2.0.0", "2.0.0-rc1", 1}, {"3", "2.99.99", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

const vulnFixture = `{"error":0,"message":null,"data":{"name":"Contact Form 7","plugin":"contact-form-7","vulnerability":[
 {"uuid":"a","name":"Contact Form 7 < 5.3.2 - Unrestricted File Upload","operator":{"min_version":null,"min_operator":null,"max_version":"5.3.2","max_operator":"lt","unfixed":"0"},
  "source":[{"id":"CVE-2020-35489","name":"CVE","link":"https://www.cve.org/CVERecord?id=CVE-2020-35489"}],
  "impact":{"cvss":{"version":"3.1","score":"10.0","severity":"c"}}},
 {"uuid":"b","name":"Contact Form 7 <= 5.8.3 - Open Redirect","operator":{"min_version":"5.0","min_operator":"ge","max_version":"5.8.3","max_operator":"le","unfixed":0},
  "source":[],"impact":{"cvss":{"score":6.1}}},
 {"uuid":"c","name":"Contact Form 7 < 5.9 - XSS","operator":{"max_version":"5.9","max_operator":"lt"},"source":{},"impact":[]},
 {"uuid":"d","name":"Contact Form 7 < 6.0 - CSRF","operator":[],"source":[],"impact":[{"cvss":{"score":"8.1","severity":"h"}}]}
]}}`

func TestVulnMatching(t *testing.T) {
	var r apiResponse
	if err := json.Unmarshal([]byte(vulnFixture), &r); err != nil {
		t.Fatal(err)
	}
	list := r.Data.Vulnerability
	if v, ok := toVuln(list[0], "plugin", "contact-form-7", "CF7", "5.3.1"); !ok || v.FixedIn != "5.3.2" || v.Severity != "critical" || v.Score != 10 || len(v.Links) != 1 {
		t.Fatalf("5.3.1 should be affected: %+v %v", v, ok)
	}
	if _, ok := toVuln(list[0], "plugin", "contact-form-7", "CF7", "5.3.2"); ok {
		t.Fatal("5.3.2 is fixed")
	}
	if v, ok := toVuln(list[1], "plugin", "contact-form-7", "CF7", "5.8.3"); !ok || v.FixedIn != "" || v.Severity != "medium" {
		t.Fatalf("5.8.3 should be affected with no known fix version: %+v", v)
	}
	if _, ok := toVuln(list[1], "plugin", "contact-form-7", "CF7", "4.9"); ok {
		t.Fatal("4.9 is below the affected range")
	}
	// PHP sends [] for an empty object; that must not break the response.
	if v, ok := toVuln(list[2], "plugin", "contact-form-7", "CF7", "5.8"); !ok || v.FixedIn != "5.9" || v.Score != 0 {
		t.Fatalf("empty impact: %+v %v", v, ok)
	}
	if v, ok := toVuln(list[3], "plugin", "contact-form-7", "CF7", "5.8"); !ok || v.Severity != "high" || v.Score != 8.1 {
		t.Fatalf("impact wrapped in an array: %+v %v", v, ok)
	}
}

func TestPHPCustomRender(t *testing.T) {
	s := model.Site{Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3",
		PHP: model.PHPSettings{MaxExecution: 120, Custom: "; comment\nopcache.enable = On\ndate.timezone = Europe/London\nerror_reporting = E_ALL & ~E_DEPRECATED"}}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	pool := render.Pool(render.Paths{SitesDir: "/srv/sites", SocketTmpl: "/run/php/berth-{site}.sock"}, s)
	for _, want := range []string{
		"php_admin_value[max_execution_time] = 120", "php_admin_value[max_input_vars] = 5000",
		"php_admin_flag[opcache.enable] = on", "php_admin_value[date.timezone] = Europe/London",
		`php_admin_value[error_reporting] = "E_ALL & ~E_DEPRECATED"`, "slowlog = /srv/sites/demo/logs/php-slow.log",
	} {
		if !strings.Contains(pool, want) {
			t.Errorf("pool lacks %q:\n%s", want, pool)
		}
	}
	for _, bad := range []string{"open_basedir = /", "foo", "x = a\"b", "memory_limit = 2G"} {
		s.PHP.Custom = bad
		if err := s.Normalize(); err == nil {
			t.Errorf("custom %q was accepted", bad)
		}
	}
	// Sites saved before PHP settings existed render the old defaults.
	old := render.Pool(render.Paths{SitesDir: "/srv/sites"}, model.Site{Name: "old", PHPVersion: "8.3"})
	if !strings.Contains(old, "max_execution_time] = 300") {
		t.Fatal("zero PHP settings should render defaults")
	}
}

func TestAutoUpdateDue(t *testing.T) {
	a := model.AutoUpdate{Enabled: true, Day: "tue", Time: "04:00", Plugins: true}
	tue := time.Date(2026, 11, 24, 4, 0, 0, 0, time.Local)
	if !a.Due(tue) || a.Due(tue.Add(time.Minute)) || a.Due(tue.AddDate(0, 0, 1)) {
		t.Fatal("weekly schedule")
	}
	a.Day = "daily"
	if !a.Due(tue.AddDate(0, 0, 3)) {
		t.Fatal("daily schedule")
	}
	s := model.Site{Name: "x", Type: model.TypeWordPress, Domain: "x.test", PHPVersion: "8.3", AutoUpdate: model.AutoUpdate{Enabled: true}}
	if err := s.Normalize(); err == nil {
		t.Fatal("an empty update plan was accepted")
	}
}

func TestTrafficIngest(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	s := model.Site{Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3", Status: model.StatusActive}
	dir := filepath.Join(e.Cfg.LogDir, "demo")
	_ = os.MkdirAll(dir, 0o755)
	now := time.Now().Truncate(time.Second)
	ts := now.Format("02/Jan/2006:15:04:05 -0700")
	lines := []string{
		fmt.Sprintf(`1.2.3.4 - - [%s] "GET /about/?x=1 HTTP/1.1" 200 5120 "https://www.google.com/search" "Mozilla/5.0 Chrome/120.0 Safari/537.36" 0.120`, ts),
		fmt.Sprintf(`1.2.3.4 - - [%s] "GET /wp-content/a.css HTTP/1.1" 200 1000 "https://demo.test/about/" "Mozilla/5.0 Chrome/120.0 Safari/537.36" 0.001`, ts),
		fmt.Sprintf(`5.6.7.8 - - [%s] "POST /checkout HTTP/2.0" 502 0 "-" "Googlebot/2.1" 2.500`, ts),
		fmt.Sprintf(`9.9.9.9 - - [%s] "GET /missing HTTP/1.1" 404 10 "-" "Mozilla/5.0 Firefox/121.0" 0.010`, ts),
		fmt.Sprintf(`127.0.0.1 - - [%s] "GET / HTTP/1.1" 200 10 "-" "Berth-Uptime/1" 0.010`, ts),
	}
	log := filepath.Join(dir, "access.log")
	_ = os.WriteFile(log, []byte(strings.Join(lines, "\n")+"\npartial"), 0o644)
	if err := e.IngestTraffic(ctx, s); err != nil {
		t.Fatal(err)
	}
	// Reading again adds nothing; the partial line waits for its newline.
	_ = e.IngestTraffic(ctx, s)
	hours, _ := e.Store.Traffic(ctx, "demo", now.Add(-time.Hour).Unix())
	if len(hours) != 1 || hours[0].Requests != 4 || hours[0].S5 != 1 || hours[0].S4 != 1 || hours[0].Bots != 1 || hours[0].Bytes != 6130 {
		t.Fatalf("hours %+v", hours)
	}
	day := now.Format("2006-01-02")
	pages, _ := e.Store.Top(ctx, "demo", "page", day, "requests", 10)
	for _, p := range pages {
		if strings.HasSuffix(p.Key, ".css") || strings.Contains(p.Key, "?") {
			t.Fatalf("page list has %q", p.Key)
		}
	}
	refs, _ := e.Store.Top(ctx, "demo", "ref", day, "requests", 10)
	if len(refs) != 1 || refs[0].Key != "google.com" {
		t.Fatalf("referrers %+v (own domain must be left out)", refs)
	}
	slow, _ := e.Store.SlowRequests(ctx, "demo", 0, 10)
	if len(slow) != 1 || slow[0].Path != "/checkout" || slow[0].MS != 2500 {
		t.Fatalf("slow %+v", slow)
	}
	uniq, _ := e.Store.DailyUnique(ctx, "demo", "ip", day)
	if uniq[day] != 3 {
		t.Fatalf("visitors %v", uniq)
	}
	// Rotation: the old file is finished, then the new one read from the start.
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("\n" + strings.Replace(lines[3], "/missing", "/gone", 1) + "\n")
	f.Close()
	_ = os.Rename(log, log+".1")
	_ = os.WriteFile(log, []byte(lines[0]+"\n"), 0o644)
	if err := e.IngestTraffic(ctx, s); err != nil {
		t.Fatal(err)
	}
	hours, _ = e.Store.Traffic(ctx, "demo", now.Add(-time.Hour).Unix())
	if hours[0].Requests != 6 {
		t.Fatalf("after rotation %+v", hours)
	}
}

func TestSlowLogs(t *testing.T) {
	php := `
[22-Nov-2026 10:00:00]  [pool berth-demo] pid 1234
script_filename = /srv/sites/demo/public/index.php
[0x00007f0000000001] curl_exec() /srv/sites/demo/public/wp-includes/Requests/src/Transport/Curl.php:205
[0x00007f0000000002] request() /srv/sites/demo/public/wp-content/plugins/slow-plugin/api.php:45
[0x00007f0000000003] {main}() /srv/sites/demo/public/index.php:17

[22-Nov-2026 10:05:00]  [pool berth-demo] pid 1240
script_filename = /srv/sites/demo/public/index.php
[0x00007f0000000004] sleep() /srv/sites/demo/public/wp-content/themes/heavy/functions.php:9
`
	en := ParsePHPSlowLog(php, "/srv/sites/demo")
	if len(en) != 2 || en[0].Culprit != "theme heavy" || en[1].Culprit != "plugin slow-plugin" || en[1].Script != "/public/index.php" {
		t.Fatalf("%+v", en)
	}
	my := `# Time: 261122 10:00:00
# User@Host: site_demo[site_demo] @ localhost []
# Thread_id: 12  Schema: site_demo  QC_hit: No
# Query_time: 2.500000  Lock_time: 0.000050  Rows_sent: 1  Rows_examined: 100000
SET timestamp=1795000000;
SELECT * FROM wp_postmeta WHERE meta_value = 'abc' AND post_id IN (1,2,3);
# User@Host: site_other[site_other] @ localhost []
# Query_time: 9.0  Lock_time: 0  Rows_sent: 1  Rows_examined: 5
SET timestamp=1795000001;
SELECT 1;
# User@Host: site_demo[site_demo] @ localhost []
# Query_time: 1.500000  Lock_time: 0.000050  Rows_sent: 1  Rows_examined: 200000
SET timestamp=1795000002;
SELECT * FROM wp_postmeta WHERE meta_value = 'xyz' AND post_id IN (4,5);
`
	q := ParseMySQLSlowLog(my, "site_demo")
	if len(q) != 1 || q[0].Count != 2 || q[0].MaxS != 2.5 || q[0].Examined != 200000 || q[0].Last != 1795000002 {
		t.Fatalf("%+v", q)
	}
}

func TestUptimeAndHealth(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	state := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch state {
		case "fatal":
			io.WriteString(w, "<p>There has been a critical error on this website.</p>")
		case "down":
			w.WriteHeader(503)
		default:
			io.WriteString(w, "<html>ok</html>")
		}
	}))
	defer srv.Close()
	e.LocalBase = srv.URL
	s := model.Site{Name: "demo", Type: model.TypeWordPress, Domain: strings.TrimPrefix(srv.URL, "http://"), PHPVersion: "8.3", Status: model.StatusActive, Enabled: true}
	if h := e.SiteHealth(ctx, s, "/"); !h.OK || h.Status != 200 {
		t.Fatalf("%+v", h)
	}
	state = "fatal"
	if h := e.SiteHealth(ctx, s, "/"); h.OK {
		t.Fatal("a WordPress critical error page counts as broken")
	}
	// Two failures in a row mark the site down; one success brings it back.
	base := time.Now().Add(-time.Hour).Unix()
	for i, ok := range []bool{true, false, true, false, false, false, true} {
		e.recordCheck(ctx, s, store.Check{Site: s.Name, Time: base + int64(i)*60, OK: ok, MS: 100, Error: map[bool]string{false: "503"}[ok]})
		st := e.uptimeState(ctx, s.Name)
		if i == 4 && !st.Down || i == 3 && st.Down || i == 6 && st.Down {
			t.Fatalf("check %d: state %+v", i, st)
		}
	}
	checks, _ := e.Store.Checks(ctx, s.Name, 0)
	inc := Incidents(checks)
	if len(inc) != 1 || inc[0].Start != base+180 || inc[0].End != base+360 {
		t.Fatalf("incidents %+v", inc)
	}
	ev, _ := e.Store.Events(ctx, s.Name, 10)
	if len(ev) != 2 || !strings.Contains(ev[0].Message, "back up") || !strings.Contains(ev[1].Message, "is down") {
		t.Fatalf("events %+v", ev)
	}
	state = "ok"
	c := e.CheckUptime(ctx, model.Site{Name: "x", Domain: s.Domain})
	if !c.OK || c.Status != 200 {
		t.Fatalf("uptime check %+v", c)
	}
}
