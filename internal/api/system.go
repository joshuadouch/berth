package api

import (
	"context"
	"net/http"
	"os"
	"strconv"

	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/model"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sites, err := s.Store.Sites(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
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
	writeJSON(w, 200, map[string]any{
		"sites": len(sites), "counts": counts, "attention": problems,
		"services": s.Engine.Services(ctx), "disks": s.Engine.Disks(ctx), "usage": s.Engine.SiteUsage(),
		"backups": len(backups), "backup_bytes": backupBytes, "jobs": jobs, "events": events,
		"settings": map[string]any{
			"backup_enabled": st.BackupEnabled, "backup_time": st.BackupTime, "trusted_proxies": st.TrustedProxies,
			"halyard": st.Halyard.URL != "" && st.Halyard.Token != "",
		},
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ev, err := s.Store.Events(r.Context(), r.URL.Query().Get("site"), 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ev)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, 200, j)
}

// Backups ----------------------------------------------------------------------

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	b, err := s.Engine.Backups(r.Context(), r.URL.Query().Get("site"))
	if err != nil {
		writeErr(w, 500, err.Error())
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
	p, err := s.Engine.BackupPath(r.Context(), r.PathValue("id"), r.PathValue("file"))
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
	if _, err := s.Engine.BackupPath(r.Context(), site.Name, file); err != nil {
		writeStoreErr(w, err)
		return
	}
	safety := r.URL.Query().Get("safety") != "0"
	id, err := s.Engine.StartJob(site.Name, "restore", "Restore "+site.Domain+" from "+file, func(ctx context.Context, logf engine.Logf) error {
		return s.Engine.Restore(ctx, site, file, safety, logf)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
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
	tokenSet := st.Halyard.Token != ""
	st.Halyard.Token = ""
	writeJSON(w, 200, map[string]any{"settings": st, "halyard_token_set": tokenSet,
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
	if err := st.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
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
	st.Halyard.Token = ""
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
		"name": s.Cfg.Name, "version": s.Version, "update": s.Engine.CheckUpdate(r.Context(), r.URL.Query().Get("check") == "1"),
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

// Halyard (filled in by the integration) ---------------------------------------

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
	site, err := s.Engine.LinkHalyard(r.Context(), site.Name, req)
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
	site, err := s.Engine.UnlinkHalyard(r.Context(), site.Name)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, site)
}
