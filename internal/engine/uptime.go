package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// downAfter is how many failed checks in a row mark a site down.
const downAfter = 2

// UptimeState is a site's current monitoring state.
type UptimeState struct {
	Down      bool      `json:"down"`
	Since     time.Time `json:"since"` // when the current state began
	Fails     int       `json:"fails"`
	LastError string    `json:"last_error,omitempty"`
	LastCheck time.Time `json:"last_check"`
	LastMS    int       `json:"last_ms"`
}

var uptimeMu sync.Mutex

func (e *Engine) uptimeState(ctx context.Context, site string) UptimeState {
	var st UptimeState
	_ = e.Store.GetKV(ctx, "uptime_"+site, &st)
	return st
}

// UptimeStates returns the monitoring state of every site.
func (e *Engine) UptimeStates(ctx context.Context, sites []model.Site) map[string]UptimeState {
	out := map[string]UptimeState{}
	for _, s := range sites {
		if st := e.uptimeState(ctx, s.Name); !st.LastCheck.IsZero() {
			out[s.Name] = st
		}
	}
	return out
}

// monitored reports whether uptime checks run for a site.
func monitored(st model.Settings, s model.Site) bool {
	return !st.NoUptime && !s.NoMonitor && s.Enabled && s.Status == model.StatusActive
}

// CheckUptime requests a site's public address, as a visitor would.
func (e *Engine) CheckUptime(ctx context.Context, s model.Site) store.Check {
	c := store.Check{Site: s.Name, Time: time.Now().Unix()}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL()+"/", nil)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	req.Header.Set("User-Agent", "Berth-Uptime/1 (+https://github.com/joshuadouch/berth)")
	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	start := time.Now()
	res, err := client.Do(req)
	c.MS = int(time.Since(start).Milliseconds())
	if err != nil {
		c.Error = shortNetErr(err)
		// Say whether the server itself answers, to tell the two problems apart.
		if h := e.SiteHealth(ctx, s, "/"); h.OK {
			c.Error += " (the site works on this server, so the problem is DNS, a proxy in front or Cloudflare)"
		}
		return c
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	c.Status = res.StatusCode
	if res.StatusCode >= 500 {
		c.Error = res.Status
		return c
	}
	for _, m := range brokenMarkers {
		if strings.Contains(string(body), m) {
			c.Error = "the page says: " + m
			return c
		}
	}
	c.OK = true
	return c
}

func shortNetErr(err error) string {
	msg := err.Error()
	// "Get \"https://x/\": dial tcp: lookup x: no such host" -> the part after the URL.
	if i := strings.Index(msg, "\": "); i >= 0 {
		msg = msg[i+3:]
	}
	return msg
}

// runUptime checks every monitored site once.
func (e *Engine) runUptime(ctx context.Context) {
	st, err := e.Store.Settings(ctx)
	if err != nil || st.NoUptime || e.Cfg.DryRun && e.HTTP == nil {
		return
	}
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, s := range sites {
		if !monitored(st, s) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(s model.Site) {
			defer wg.Done()
			defer func() { <-sem }()
			e.recordCheck(ctx, s, e.CheckUptime(ctx, s))
		}(s)
	}
	wg.Wait()
}

// recordCheck stores a check and logs when a site goes down or comes back.
func (e *Engine) recordCheck(ctx context.Context, s model.Site, c store.Check) {
	_ = e.Store.AddCheck(ctx, c)
	uptimeMu.Lock()
	defer uptimeMu.Unlock()
	st := e.uptimeState(ctx, s.Name)
	now := time.Unix(c.Time, 0).UTC()
	if st.LastCheck.IsZero() {
		st.Since = now
	}
	st.LastCheck, st.LastMS = now, c.MS
	if c.OK {
		if st.Down {
			e.Store.AddEvent(ctx, "info", "uptime", s.Name, fmt.Sprintf("%s is back up after %s", s.Domain, roundDur(now.Sub(st.Since))))
			st.Down, st.Since = false, now
		}
		st.Fails, st.LastError = 0, ""
	} else {
		st.Fails++
		st.LastError = c.Error
		if !st.Down && st.Fails >= downAfter {
			st.Down, st.Since = true, now
			e.Store.AddEvent(ctx, "error", "uptime", s.Name, s.Domain+" is down: "+c.Error)
		}
	}
	_ = e.Store.SetKV(ctx, "uptime_"+s.Name, st)
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f h", d.Hours())
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// Incident is a stretch of failed checks.
type Incident struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"` // 0 while ongoing
	Error string `json:"error"`
}

// Incidents finds outages (two or more failed checks in a row) in checks.
func Incidents(checks []store.Check) []Incident {
	out := []Incident{}
	var cur *Incident
	fails := 0
	var firstFail store.Check
	for _, c := range checks {
		if !c.OK {
			if fails == 0 {
				firstFail = c
			}
			fails++
			if fails == downAfter {
				out = append(out, Incident{Start: firstFail.Time, Error: firstFail.Error})
				cur = &out[len(out)-1]
			}
			continue
		}
		if cur != nil {
			cur.End = c.Time
			cur = nil
		}
		fails = 0
	}
	// Newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
