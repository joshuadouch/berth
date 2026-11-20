package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/model"
)

type siteRow struct {
	model.Site
	Usage      engine.Usage  `json:"usage"`
	LastBackup *model.Backup `json:"last_backup"`
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.Store.Sites(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	usage := s.Engine.SiteUsage()
	backups, _ := s.Engine.Backups(r.Context(), "")
	last := map[string]*model.Backup{}
	for i := range backups {
		if last[backups[i].Site] == nil {
			last[backups[i].Site] = &backups[i]
		}
	}
	out := make([]siteRow, 0, len(sites))
	for _, site := range sites {
		out = append(out, siteRow{Site: site, Usage: usage[site.Name], LastBackup: last[site.Name]})
	}
	writeJSON(w, 200, out)
}

func (s *Server) site(w http.ResponseWriter, r *http.Request) (model.Site, bool) {
	site, err := s.Store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return site, false
	}
	return site, true
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	backups, _ := s.Engine.Backups(r.Context(), site.Name)
	jobs, _ := s.Store.Jobs(r.Context(), site.Name, 15)
	events, _ := s.Store.Events(r.Context(), site.Name, 30)
	writeJSON(w, 200, map[string]any{
		"site": site, "usage": s.Engine.SiteUsage()[site.Name], "backups": backups, "jobs": jobs, "events": events,
		"dir": s.Engine.Paths().SiteDir(site.Name), "user": site.User(), "socket": s.Cfg.Socket(site.Name),
		"sftp_available": s.Cfg.SFTP.Enabled,
	})
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
	var req engine.CreateRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	res, err := s.Engine.CreateSite(r.Context(), req, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) updateSite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		model.Site
		ReplaceURLs bool `json:"replace_urls"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Site.ID = r.PathValue("id")
	site, job, err := s.Engine.UpdateSite(r.Context(), req.Site, req.ReplaceURLs, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"site": site, "job_id": job})
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	keep := r.URL.Query().Get("backup") != "0"
	id, err := s.Engine.DeleteSite(r.Context(), r.PathValue("id"), keep, currentUser(r).Username)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) retrySite(w http.ResponseWriter, r *http.Request) {
	id, pw, err := s.Engine.RetryProvision(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id, "admin_password": pw})
}

func (s *Server) fixPermissions(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	if err := s.Engine.FixPermissions(r.Context(), site); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "site", site.Name, "Fixed file ownership")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// siteLogs returns the end of a log file.
func (s *Server) siteLogs(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	var path string
	switch r.URL.Query().Get("file") {
	case "", "access":
		path = filepath.Join(s.Cfg.LogDir, site.Name, "access.log")
	case "error":
		path = filepath.Join(s.Cfg.LogDir, site.Name, "error.log")
	case "php":
		path = filepath.Join(s.Engine.Paths().SiteDir(site.Name), "logs", "php-error.log")
	default:
		writeErr(w, 400, "unknown log")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 300
	}
	lines, err := tail(path, n)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"path": path, "lines": lines})
}

// tail reads the last n lines of a file, looking at most 2 MB back.
func tail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return []string{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const max = 2 << 20
	if st.Size() > max {
		if _, err := f.Seek(st.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	ring := make([]string, 0, n)
	first := st.Size() > max
	for sc.Scan() {
		if first {
			first = false // partial line
			continue
		}
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	return ring, nil
}

func (s *Server) rotateDBPassword(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	if site.DB == nil {
		writeErr(w, 400, "this site has no database")
		return
	}
	old := site.DB.Password
	site.DB.Password = engine.Password(24)
	if err := s.Engine.SetDBPassword(r.Context(), site); err != nil {
		site.DB.Password = old
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "site", site.Name, "Database password changed")
	writeJSON(w, 200, map[string]any{"password": site.DB.Password})
}

func (s *Server) sftpPassword(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	_ = readJSON(r, &req)
	if req.Password == "" {
		req.Password = engine.Password(20)
	}
	if !site.SFTP.Enabled {
		writeErr(w, 400, "turn SFTP on for this site first")
		return
	}
	if err := s.Engine.SetSFTPPassword(r.Context(), site, req.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	site.SFTP.PasswordSet = true
	if err := s.Store.SaveSite(r.Context(), site); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "site", site.Name, "SFTP password set")
	writeJSON(w, 200, map[string]any{"password": req.Password, "user": site.User()})
}

func (s *Server) wpSite(w http.ResponseWriter, r *http.Request) (model.Site, bool) {
	site, ok := s.site(w, r)
	if !ok {
		return site, false
	}
	if site.Type != model.TypeWordPress || site.Status != model.StatusActive {
		writeErr(w, 400, "this is not an active WordPress site")
		return site, false
	}
	return site, true
}

func (s *Server) wpInfo(w http.ResponseWriter, r *http.Request) {
	site, ok := s.wpSite(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.Engine.WordPressInfo(r.Context(), site))
}

func (s *Server) wpUpdate(w http.ResponseWriter, r *http.Request) {
	site, ok := s.wpSite(w, r)
	if !ok {
		return
	}
	backup := r.URL.Query().Get("backup") != "0"
	id, err := s.Engine.StartJob(site.Name, "wp-update", "Update WordPress on "+site.Domain, func(ctx context.Context, logf engine.Logf) error {
		if backup {
			logf("Backing up first")
			if _, err := s.Engine.Backup(ctx, site, logf); err != nil {
				return fmt.Errorf("backup failed, so nothing was updated: %w", err)
			}
		}
		return s.Engine.UpdateWordPress(ctx, site, logf)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"job_id": id})
}

func (s *Server) wpAdminPassword(w http.ResponseWriter, r *http.Request) {
	site, ok := s.wpSite(w, r)
	if !ok {
		return
	}
	var req struct {
		User string `json:"user"`
	}
	_ = readJSON(r, &req)
	pw, err := s.Engine.ResetWPAdmin(r.Context(), site, strings.TrimSpace(req.User))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	login := req.User
	if login == "" {
		login = site.WP.AdminUser
	}
	s.Store.AddEvent(r.Context(), "info", "site", site.Name, "WordPress password reset for "+login)
	writeJSON(w, 200, map[string]any{"user": login, "password": pw, "login_url": site.URL() + "/wp-admin/"})
}

func (s *Server) siteNginx(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	conf, err := s.Engine.SiteNginx(r.Context(), site)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"config": conf, "custom": site.NginxCustom})
}

func (s *Server) setSiteNginx(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	var in struct {
		Custom string `json:"custom"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	next, err := s.Engine.SetNginxCustom(r.Context(), site.Name, in.Custom, currentUser(r).Username)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	conf, _ := s.Engine.SiteNginx(r.Context(), next)
	writeJSON(w, 200, map[string]any{"config": conf, "custom": next.NginxCustom})
}
