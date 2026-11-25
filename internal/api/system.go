package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/model"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sites, err := s.visibleSites(r)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !currentUser(r).IsAdmin() {
		s.customerDashboard(w, r, sites)
		return
	}
	counts := map[string]int{}
	problems := []model.Site{}
	for _, site := range sites {
		counts[string(site.Type)]++
		if site.Status == model.StatusError || !site.Enabled {
			problems = append(problems, site)
		}
	}
	backups, _ := s.Engine.Backups(ctx, "")
	var backupBytes int64
	for _, b := range backups {
		backupBytes += b.Size
	}
	jobs, _ := s.Store.Jobs(ctx, "", 8)
	events, _ := s.Store.Events(ctx, "", 12)
	st, _ := s.Store.Settings(ctx)
	since := time.Now().Add(-24 * time.Hour)
	traffic, _ := s.Store.Traffic(ctx, "", since.Unix()/3600*3600)
	uptime, _ := s.Store.UptimeSince(ctx, since.Unix())
	res, _ := s.Engine.ResourceHistory(ctx, "", 24*time.Hour)
	writeJSON(w, 200, map[string]any{
		"traffic": traffic, "uptime": uptime, "uptime_state": s.Engine.UptimeStates(ctx, sites),
		"vulns": s.Engine.VulnCounts(ctx, sites), "resources": res,
		"sites": len(sites), "counts": counts, "attention": problems,
		"services": s.Engine.Services(ctx), "disks": s.Engine.Disks(ctx), "usage": s.Engine.SiteUsage(),
		"backups": len(backups), "backup_bytes": backupBytes, "jobs": jobs, "events": events,
		"settings": map[string]any{
			"backup_enabled": st.BackupEnabled, "backup_time": st.BackupTime, "trusted_proxies": st.TrustedProxies,
			"halyard": st.Halyard.URL != "" && st.Halyard.Token != "",
		},
	})
}

// customerDashboard is the dashboard of a customer: their sites only, and
// nothing about the server.
func (s *Server) customerDashboard(w http.ResponseWriter, r *http.Request, sites []model.Site) {
	ctx := r.Context()
	names := siteNames(sites)
	mine := map[string]bool{}
	for _, n := range names {
		mine[n] = true
	}
	counts := map[string]int{}
	problems := []model.Site{}
	for _, site := range sites {
		counts[string(site.Type)]++
		if site.Status == model.StatusError || !site.Enabled {
			problems = append(problems, site)
		}
	}
	all, _ := s.Engine.Backups(ctx, "")
	backups := 0
	var backupBytes int64
	for _, b := range all {
		if mine[b.Site] {
			backups++
			backupBytes += b.Size
		}
	}
	usage := map[string]engine.Usage{}
	for n, u := range s.Engine.SiteUsage() {
		if mine[n] {
			usage[n] = u
		}
	}
	jobs, _ := s.Store.JobsIn(ctx, names, 8)
	events, _ := s.Store.EventsIn(ctx, names, 12)
	since := time.Now().Add(-24 * time.Hour)
	traffic, _ := s.Store.TrafficIn(ctx, names, since.Unix()/3600*3600)
	upAll, _ := s.Store.UptimeSince(ctx, since.Unix())
	uptime := map[string]any{}
	for n, u := range upAll {
		if mine[n] {
			uptime[n] = u
		}
	}
	writeJSON(w, 200, map[string]any{
		"traffic": traffic, "uptime": uptime, "uptime_state": s.Engine.UptimeStates(ctx, sites),
		"vulns": s.Engine.VulnCounts(ctx, sites), "resources": []any{},
		"sites": len(sites), "counts": counts, "attention": problems,
		"services": []any{}, "disks": []any{}, "usage": usage,
		"backups": backups, "backup_bytes": backupBytes, "jobs": jobs, "events": events,
		"settings": map[string]any{"backup_enabled": false, "backup_time": "", "trusted_proxies": []string{}, "halyard": false},
		"customer": true, "plan": s.planUsage(ctx, currentUser(r).AccountID),
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if !currentUser(r).IsAdmin() {
		s.scoped(w, r, site, func(names []string) (any, error) { return s.Store.EventsIn(r.Context(), names, 200) })
		return
	}
	ev, err := s.Store.Events(r.Context(), site, 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ev)
}

// scoped answers a customer's request for logs of one of their sites, or of
// all of them.
func (s *Server) scoped(w http.ResponseWriter, r *http.Request, site string, f func(names []string) (any, error)) {
	var names []string
	if site != "" {
		if !s.mayOpenSiteName(r, site) {
			writeErr(w, 404, "not found")
			return
		}
		names = []string{site}
	} else {
		sites, err := s.visibleSites(r)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		names = siteNames(sites)
	}
	v, err := f(names)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin() {
		s.scoped(w, r, r.URL.Query().Get("site"), func(names []string) (any, error) { return s.Store.JobsIn(r.Context(), names, 100) })
		return
	}
	jobs, err := s.Store.Jobs(r.Context(), r.URL.Query().Get("site"), 100)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, jobs)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	j, err := s.Store.Job(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// The job's own account counts too: once a delete job removes the site,
	// its owner must still be able to watch the job finish.
	u := currentUser(r)
	if !s.mayOpenSiteName(r, j.Site) && (j.AccountID == "" || j.AccountID != u.AccountID) {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, j)
}

// Backups ----------------------------------------------------------------------

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if site != "" && !s.mayOpenSiteName(r, site) {
		writeErr(w, 404, "not found")
		return
	}
	b, err := s.Engine.Backups(r.Context(), site)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !currentUser(r).IsAdmin() {
		sites, _ := s.visibleSites(r)
		mine := map[string]bool{}
		for _, n := range siteNames(sites) {
			mine[n] = true
		}
		out := b[:0:0]
		for _, x := range b {
			if mine[x.Site] {
				out = append(out, x)
			}
		}
		st, _ := s.Store.Settings(r.Context())
		writeJSON(w, 200, map[string]any{"backups": out, "dir": "", "keep": st.BackupKeep, "enabled": st.BackupEnabled, "time": st.BackupTime})
		return
	}
	st, _ := s.Store.Settings(r.Context())
	writeJSON(w, 200, map[string]any{"backups": b, "dir": st.BackupDir, "keep": st.BackupKeep,
		"enabled": st.BackupEnabled, "time": st.BackupTime})
}

func (s *Server) runNightly(w http.ResponseWriter, r *http.Request) {
	id, err := s.Engine.StartJob("", "backup", "Back up all sites", func(ctx context.Context, logf engine.Logf) error {
		return s.Engine.NightlyBackups(ctx, logf)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	if site.Status != model.StatusActive {
		writeErr(w, 400, "only an active site can be backed up")
		return
	}
	id, err := s.Engine.StartJob(site.Name, "backup", "Back up "+site.Domain, func(ctx context.Context, logf engine.Logf) error {
		_, err := s.Engine.Backup(ctx, site, logf)
		return err
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	p, err := s.Engine.LocalBackup(r.Context(), r.PathValue("id"), r.PathValue("file"), func(string, ...any) {})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.PathValue("file")+`"`)
	http.ServeContent(w, r, r.PathValue("file"), st.ModTime(), f)
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	file := r.PathValue("file")
	if !engine.ValidBackupName(file) || !strings.HasPrefix(file, site.Name+"-") {
		writeErr(w, 400, "invalid backup name")
		return
	}
	var req struct {
		Safety *bool    `json:"safety"`
		What   string   `json:"what"`  // all, files, db
		Paths  []string `json:"paths"` // restore only these files and folders
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	o := engine.RestoreOptions{Safety: req.Safety == nil || *req.Safety, What: req.What}
	if r.URL.Query().Get("safety") == "0" {
		o.Safety = false
	}
	title := "Restore " + site.Domain + " from " + file
	if len(req.Paths) > 0 {
		title = fmt.Sprintf("Restore %d item(s) of %s from %s", len(req.Paths), site.Domain, file)
	}
	id, err := s.Engine.StartJob(site.Name, "restore", title, func(ctx context.Context, logf engine.Logf) error {
		if len(req.Paths) > 0 {
			return s.Engine.RestoreFiles(ctx, site, file, req.Paths, logf)
		}
		return s.Engine.Restore(ctx, site, file, o, logf)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) browseBackup(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	l, err := s.Engine.BrowseBackup(r.Context(), site.Name, r.PathValue("file"), r.URL.Query().Get("path"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, l)
}

func (s *Server) backupFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(path.Base(p), `"`, "")+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := s.Engine.BackupFile(r.Context(), site.Name, r.PathValue("file"), p, w); err != nil {
		w.Header().Del("Content-Disposition")
		writeStoreErr(w, err)
	}
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.DeleteBackup(r.Context(), r.PathValue("id"), r.PathValue("file")); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.Store.AddEvent(r.Context(), "info", "backup", r.PathValue("id"), "Deleted backup "+r.PathValue("file"))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// Settings ---------------------------------------------------------------------

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Settings(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if u := currentUser(r); !u.IsAdmin() {
		// Customers only need the defaults for new sites.
		email := u.Email
		if a, err := s.Store.Account(r.Context(), u.AccountID); err == nil && email == "" {
			email = a.Email
		}
		view := model.Settings{DefaultPHP: st.DefaultPHP, WPLocale: st.WPLocale, WPAdminUser: st.WPAdminUser, WPAdminEmail: email}
		writeJSON(w, 200, map[string]any{"settings": view, "installed_php": s.Engine.InstalledPHP(), "php_versions": model.PHPVersions})
		return
	}
	tokenSet := st.Halyard.Token != ""
	secretSet := st.S3.SecretKey != ""
	st.Halyard.Token, st.S3.SecretKey, st.CloudflareToken = "", "", ""
	writeJSON(w, 200, map[string]any{"settings": st, "halyard_token_set": tokenSet, "s3_secret_set": secretSet,
		"s3_error":      s.Engine.RemoteError(r.Context()),
		"installed_php": s.Engine.InstalledPHP(), "php_versions": model.PHPVersions})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st model.Settings
	if err := readJSON(r, &st); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cur, err := s.Store.Settings(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if st.Halyard.Token == "" {
		st.Halyard.Token = cur.Halyard.Token // blank keeps the saved token
	} else if st.Halyard.Token == "-" {
		st.Halyard.Token = ""
	}
	if st.S3.SecretKey == "" {
		st.S3.SecretKey = cur.S3.SecretKey
	}
	// Cloudflare lives in its plugin now; keep whatever is left here as is.
	st.CloudflareToken, st.AccessTeam, st.AccessAUD, st.AccessEmails = cur.CloudflareToken, cur.AccessTeam, cur.AccessAUD, cur.AccessEmails
	// The logo and favicon change through their own upload routes.
	st.Brand.Logo, st.Brand.Favicon = cur.Brand.Logo, cur.Brand.Favicon
	if err := st.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if st.S3.Enabled && st.S3.SecretKey == "" {
		writeErr(w, 400, "S3 needs the secret key")
		return
	}
	if err := s.Store.SetSettings(r.Context(), st); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.Engine.Apply(r.Context()); err != nil {
		_ = s.Store.SetSettings(r.Context(), cur)
		_ = s.Engine.Apply(r.Context())
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "settings", "", currentUser(r).Username+" changed settings")
	st.Halyard.Token, st.S3.SecretKey, st.CloudflareToken = "", "", ""
	writeJSON(w, 200, st)
}

// System -----------------------------------------------------------------------

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	sites, _ := s.Store.Sites(r.Context())
	inUse := map[string]int{}
	for _, site := range sites {
		inUse[site.PHPVersion]++
	}
	writeJSON(w, 200, map[string]any{
		"name": s.Cfg.Name, "version": s.Version, "update": s.Engine.CheckUpdate(r.Context(), false),
		"php_updates":  s.Engine.PHPUpdateStatus(),
		"php_versions": model.PHPVersions, "installed_php": s.Engine.InstalledPHP(), "php_in_use": inUse,
		"services": s.Engine.Services(r.Context()), "disks": s.Engine.Disks(r.Context()),
		"paths": map[string]string{"sites": s.Cfg.SitesDir, "logs": s.Cfg.LogDir, "nginx": s.Cfg.Nginx.ConfDir, "data": s.Cfg.DataDir},
		"sftp":  s.Cfg.SFTP.Enabled,
	})
}

func (s *Server) installPHP(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("v")
	if !model.SupportedPHP(v) {
		writeErr(w, 400, "unsupported PHP version")
		return
	}
	id, err := s.Engine.StartJob("", "php", "Install PHP "+v, func(ctx context.Context, logf engine.Logf) error {
		return s.Engine.InstallPHP(ctx, v, logf)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) checkPHPUpdates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Engine.CheckPHPUpdates(r.Context()))
}

func (s *Server) applyPHPUpdates(w http.ResponseWriter, r *http.Request) {
	id, err := s.Engine.StartJob("", "php", "Install PHP updates", func(ctx context.Context, logf engine.Logf) error {
		return s.Engine.UpdatePHP(ctx, logf)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "php", "", currentUser(r).Username+" started PHP updates")
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) reapply(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Apply(r.Context()); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) startUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	_ = readJSON(r, &req)
	if err := s.Engine.StartSelfUpdate(req.Version); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "update", "", currentUser(r).Username+" started an update")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// Halyard ----------------------------------------------------------------------

func (s *Server) halyardInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Engine.HalyardInfo(r.Context()))
}

func (s *Server) halyardSync(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	var req model.HalyardLink
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	site, err := s.Engine.LinkHalyard(r.Context(), site.Name, req, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, site)
}

func (s *Server) halyardUnlink(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	site, err := s.Engine.UnlinkHalyard(r.Context(), site.Name, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, site)
}

// halyardMove moves a site off Halyard so Berth serves it directly.
func (s *Server) halyardMove(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	id, err := s.Engine.MoveOffHalyard(r.Context(), site.Name, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"job_id": id})
}

// halyardMoveAll moves every site off Halyard.
func (s *Server) halyardMoveAll(w http.ResponseWriter, r *http.Request) {
	id, err := s.Engine.MoveAllOffHalyard(r.Context(), currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"job_id": id})
}

func (s *Server) halyardPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	p, err := s.Engine.CreateHalyardPool(r.Context(), req.Address)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "halyard", "", currentUser(r).Username+" created the Halyard pool "+p.Name)
	writeJSON(w, 200, p)
}

func (s *Server) testS3(w http.ResponseWriter, r *http.Request) {
	var c model.S3Settings
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if c.SecretKey == "" {
		cur, _ := s.Store.Settings(r.Context())
		c.SecretKey = cur.S3.SecretKey
	}
	c.Enabled = true
	st := model.Settings{S3: c}
	if err := st.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Engine.TestS3(r.Context(), st.S3); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// checkUpdate asks GitHub for the latest release now.
func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Engine.CheckUpdate(r.Context(), true))
}
