package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

// Health is what a request to a site through the local nginx returned.
type Health struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	MS     int    `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// brokenMarkers are pages PHP apps answer with 200 although they are broken.
var brokenMarkers = []string{
	"There has been a critical error on this website",
	"Error establishing a database connection",
	"Fatal error</b>:",
	"PHP Fatal error",
	"Briefly unavailable for scheduled maintenance",
}

func (e *Engine) httpClient(timeout time.Duration) *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// localBase is where nginx serves sites on this machine.
func (e *Engine) localBase() string {
	if e.LocalBase != "" {
		return e.LocalBase
	}
	return "http://127.0.0.1"
}

// SiteHealth requests a path of the site from the local nginx, as a visitor
// coming through Halyard would.
func (e *Engine) SiteHealth(ctx context.Context, s model.Site, path string) Health {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", e.localBase()+path, nil)
	if err != nil {
		return Health{Error: err.Error()}
	}
	req.Host = s.Domain
	req.Header.Set("User-Agent", "Berth-Health/1")
	if s.HTTPS {
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	start := time.Now()
	res, err := e.httpClient(30 * time.Second).Do(req)
	h := Health{MS: int(time.Since(start).Milliseconds())}
	if err != nil {
		h.Error = err.Error()
		return h
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	h.Status = res.StatusCode
	switch {
	case res.StatusCode >= 500:
		h.Error = res.Status
	default:
		for _, m := range brokenMarkers {
			if strings.Contains(string(body), m) {
				h.Error = "the page says: " + m
				break
			}
		}
	}
	h.OK = h.Error == ""
	return h
}

// wpHealth checks the home page and the login page.
func (e *Engine) wpHealth(ctx context.Context, s model.Site) Health {
	h := e.SiteHealth(ctx, s, "/")
	if !h.OK {
		return h
	}
	if l := e.SiteHealth(ctx, s, "/wp-login.php"); !l.OK {
		l.Error = "login page: " + l.Error
		return l
	}
	return h
}

// UpdatePlan says what an update run changes.
type UpdatePlan struct {
	Core, Plugins, Themes bool
	// Rollback restores the backup if the site breaks.
	Rollback bool
	Backup   bool
}

// SafeUpdate backs up, updates WordPress, checks the site still works and,
// if it broke, restores the backup.
func (e *Engine) SafeUpdate(ctx context.Context, s model.Site, p UpdatePlan, logf Logf) error {
	if p.Rollback {
		p.Backup = true
	}
	before := e.wpHealth(ctx, s)
	if before.OK {
		logf("Site check before updating: OK (HTTP %d, %d ms)", before.Status, before.MS)
	} else {
		logf("Site check before updating failed: %s. It will not be rolled back, since it was not working to begin with.", before.Error)
	}
	var backup model.Backup
	if p.Backup {
		logf("Backing up first")
		b, err := e.Backup(ctx, s, logf)
		if err != nil {
			return fmt.Errorf("backup failed, so nothing was updated: %w", err)
		}
		backup = b
	}
	var steps [][]string
	if p.Core {
		steps = append(steps, []string{"core", "update"}, []string{"core", "update-db"})
	}
	if p.Plugins {
		steps = append(steps, []string{"plugin", "update", "--all"})
	}
	if p.Themes {
		steps = append(steps, []string{"theme", "update", "--all"})
	}
	steps = append(steps, []string{"language", "core", "update"}, []string{"language", "plugin", "update", "--all"}, []string{"language", "theme", "update", "--all"})
	var updErr error
	for _, st := range steps {
		logf("wp %s", strings.Join(st, " "))
		out, err := e.wp(ctx, s, "", st...)
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				logf("  %s", l)
			}
		}
		if err != nil && st[0] != "language" {
			updErr = err
			break
		}
	}
	after := e.wpHealth(ctx, s)
	if after.OK {
		logf("Site check after updating: OK (HTTP %d, %d ms)", after.Status, after.MS)
		return updErr
	}
	logf("Site check after updating failed: %s", after.Error)
	if !before.OK || !p.Rollback || backup.File == "" {
		if updErr != nil {
			return updErr
		}
		return fmt.Errorf("the site check failed after updating: %s", after.Error)
	}
	logf("Rolling back to %s", backup.File)
	if err := e.Restore(ctx, s, backup.File, RestoreOptions{What: "all"}, logf); err != nil {
		return fmt.Errorf("the update broke the site (%s) and rolling back failed: %w", after.Error, err)
	}
	again := e.wpHealth(ctx, s)
	logf("Site check after rolling back: %s", map[bool]string{true: "OK", false: "still failing: " + again.Error}[again.OK])
	e.Store.AddEvent(ctx, "error", "wp-update", s.Name, "Updates broke "+s.Domain+" ("+after.Error+"), so Berth rolled it back")
	return fmt.Errorf("the update broke the site (%s), so it was rolled back to the backup taken before", after.Error)
}

// runAutoUpdates starts scheduled WordPress updates that are due.
func (e *Engine) runAutoUpdates(ctx context.Context, now time.Time) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	for _, s := range sites {
		a := s.AutoUpdate
		if s.Type != model.TypeWordPress || s.Status != model.StatusActive || !a.Due(now) {
			continue
		}
		key := "autoupdate_last_" + s.Name
		var last string
		_ = e.Store.GetKV(ctx, key, &last)
		stamp := now.Format("2006-01-02 15:04")
		if last == stamp {
			continue
		}
		_ = e.Store.SetKV(ctx, key, stamp)
		site := s
		_, _ = e.StartJob(s.Name, "wp-update", "Automatic updates for "+s.Domain, func(ctx context.Context, logf Logf) error {
			return e.SafeUpdate(ctx, site, UpdatePlan{Core: a.Core, Plugins: a.Plugins, Themes: a.Themes, Rollback: a.Rollback, Backup: true}, logf)
		})
	}
}

// SetAutoUpdate saves a site's update schedule.
func (e *Engine) SetAutoUpdate(ctx context.Context, name string, a model.AutoUpdate, author string) (model.Site, error) {
	unlock := e.lockSite(name)
	defer unlock()
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return s, err
	}
	if s.Type != model.TypeWordPress {
		return s, fmt.Errorf("automatic updates are for WordPress sites")
	}
	s.AutoUpdate = a
	if err := s.Normalize(); err != nil {
		return s, err
	}
	if err := e.Store.SaveSite(ctx, s); err != nil {
		return s, err
	}
	msg := "turned off automatic updates"
	if s.AutoUpdate.Enabled {
		msg = "scheduled automatic updates (" + s.AutoUpdate.Day + " at " + s.AutoUpdate.Time + ")"
	}
	e.Store.AddEvent(ctx, "info", "site", name, author+" "+msg)
	return s, nil
}
