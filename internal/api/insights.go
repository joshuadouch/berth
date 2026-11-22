package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// Kinsta-style site tools: monitoring, analytics, performance, security and
// a few WordPress tools.

// rangeParam reads ?range=24h|7d|30d (default 24h).
func rangeParam(r *http.Request) time.Duration {
	switch r.URL.Query().Get("range") {
	case "1h":
		return time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// Cloudflare -------------------------------------------------------------------

func (s *Server) purgeCloudflare(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	n, err := s.Engine.PurgeCloudflare(r.Context(), site)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "site", site.Name, currentUser(r).Username+" cleared Cloudflare's cache for "+site.Domain)
	writeJSON(w, 200, map[string]any{"ok": true, "hosts": n})
}

func (s *Server) testCloudflare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	_ = readJSON(r, &req)
	if req.Token == "" {
		st, _ := s.Store.Settings(r.Context())
		req.Token = st.CloudflareToken
	}
	if req.Token == "" {
		writeErr(w, 400, "enter a token first")
		return
	}
	msg, err := s.Engine.TestCloudflare(r.Context(), strings.TrimSpace(req.Token))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"message": msg})
}

// WordPress tools ----------------------------------------------------------------

func (s *Server) wpSearchReplace(w http.ResponseWriter, r *http.Request) {
	site, ok := s.wpSite(w, r)
	if !ok {
		return
	}
	var o engine.SearchReplaceOptions
	if err := readJSON(r, &o); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if o.DryRun {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		out, err := s.Engine.WPSearchReplace(ctx, site, o, nil)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"output": out})
		return
	}
	if err := o.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	user := currentUser(r).Username
	id, err := s.Engine.StartJob(site.Name, "search-replace", "Search and replace on "+site.Domain, func(ctx context.Context, logf engine.Logf) error {
		if o.Backup {
			logf("Backing up first")
			if _, err := s.Engine.Backup(ctx, site, logf); err != nil {
				return fmt.Errorf("backup failed, so nothing was changed: %w", err)
			}
		}
		out, err := s.Engine.WPSearchReplace(ctx, site, o, logf)
		for _, l := range strings.Split(out, "\n") {
			if strings.TrimSpace(l) != "" {
				logf("  %s", l)
			}
		}
		if err == nil {
			s.Store.AddEvent(ctx, "info", "site", site.Name, fmt.Sprintf("%s replaced %q with %q in the database", user, o.From, o.To))
		}
		return err
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) setAutoUpdate(w http.ResponseWriter, r *http.Request) {
	var a model.AutoUpdate
	if err := readJSON(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	site, err := s.Engine.SetAutoUpdate(r.Context(), r.PathValue("id"), a, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, site)
}

// Security -----------------------------------------------------------------------

func (s *Server) siteVulns(w http.ResponseWriter, r *http.Request) {
	site, ok := s.wpSite(w, r)
	if !ok {
		return
	}
	rep, _ := s.Engine.VulnReportFor(r.Context(), site.Name)
	if rep.Vulns == nil {
		rep.Vulns = []engine.Vuln{}
	}
	writeJSON(w, 200, rep)
}

func (s *Server) scanVulns(w http.ResponseWriter, r *http.Request) {
	site, ok := s.wpSite(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	writeJSON(w, 200, s.Engine.RunVulnScan(ctx, site))
}

// Uptime -------------------------------------------------------------------------

type uptimeBucket struct {
	Time   int64   `json:"time"`
	Checks int     `json:"checks"`
	Up     int     `json:"up"`
	AvgMS  float64 `json:"avg_ms"`
}

func bucketChecks(checks []store.Check, since, step int64) []uptimeBucket {
	n := int((time.Now().Unix() - since + step - 1) / step)
	out := make([]uptimeBucket, n)
	for i := range out {
		out[i].Time = since + int64(i)*step
	}
	sum := make([]float64, n)
	for _, c := range checks {
		i := int((c.Time - since) / step)
		if i < 0 || i >= n {
			continue
		}
		out[i].Checks++
		if c.OK {
			out[i].Up++
			sum[i] += float64(c.MS)
		}
	}
	for i := range out {
		if out[i].Up > 0 {
			out[i].AvgMS = sum[i] / float64(out[i].Up)
		}
	}
	return out
}

func (s *Server) siteUptime(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	span := rangeParam(r)
	since := time.Now().Add(-span).Unix()
	checks, err := s.Store.Checks(ctx, site.Name, since)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	up := 0
	var ms float64
	for _, c := range checks {
		if c.OK {
			up++
			ms += float64(c.MS)
		}
	}
	sum := store.UptimeSummary{Checks: len(checks), Up: up}
	if up > 0 {
		sum.AvgMS = ms / float64(up)
	}
	st, _ := s.Store.Settings(ctx)
	recent := checks
	if len(recent) > 20 {
		recent = recent[len(recent)-20:]
	}
	writeJSON(w, 200, map[string]any{
		"state": s.Engine.UptimeStates(ctx, []model.Site{site})[site.Name], "summary": sum,
		"buckets": bucketChecks(checks, since, int64(span.Seconds())/96), "incidents": engine.Incidents(checks),
		"recent": recent, "monitored": !st.NoUptime && !site.NoMonitor, "global_off": st.NoUptime, "url": site.URL() + "/",
	})
}

// Analytics ------------------------------------------------------------------------

func (s *Server) siteTraffic(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	span := rangeParam(r)
	// Catch up on the log first, so the numbers include the last few minutes.
	_ = s.Engine.IngestTraffic(ctx, site)
	since := time.Now().Add(-span)
	hours, err := s.Store.Traffic(ctx, site.Name, since.Unix()/3600*3600)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	day := since.Local().Format("2006-01-02")
	top := map[string][]store.TopEntry{}
	for _, k := range []string{"page", "ip", "ref", "browser", "404", "5xx"} {
		top[k], _ = s.Store.Top(ctx, site.Name, k, day, "requests", 15)
	}
	top["bandwidth"], _ = s.Store.Top(ctx, site.Name, "page", day, "bytes", 10)
	uniq, _ := s.Store.DailyUnique(ctx, site.Name, "ip", day)
	writeJSON(w, 200, map[string]any{"hours": hours, "top": top, "visitors": uniq})
}

func (s *Server) sitePerformance(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	_ = s.Engine.IngestTraffic(ctx, site)
	since := time.Now().Add(-rangeParam(r))
	slow, _ := s.Store.SlowRequests(ctx, site.Name, since.Unix(), 50)
	day := since.Local().Format("2006-01-02")
	slowest, _ := s.Store.Top(ctx, site.Name, "page", day, "ms", 15)
	// Pages hit only once or twice are noise in "slowest on average".
	var avg []store.TopEntry
	for _, e := range slowest {
		if e.Requests >= 3 || len(slowest) < 5 {
			avg = append(avg, e)
		}
	}
	if avg == nil {
		avg = []store.TopEntry{}
	}
	hours, _ := s.Store.Traffic(ctx, site.Name, since.Unix()/3600*3600)
	writeJSON(w, 200, map[string]any{"slow_requests": slow, "slowest_pages": avg, "hours": hours, "logs": s.Engine.SitePerformance(ctx, site)})
}

func (s *Server) siteResources(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	m, err := s.Engine.ResourceHistory(r.Context(), site.Name, rangeParam(r))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"metrics": m, "workers": site.MaxChildren, "memory_mb": site.MemoryMB})
}

func (s *Server) serverResources(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.ResourceHistory(r.Context(), "", rangeParam(r))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"metrics": m})
}
