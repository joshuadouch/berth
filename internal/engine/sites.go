package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

// CreateRequest is what the New site form sends.
type CreateRequest struct {
	model.Site
	// WPAdminPassword is generated when empty and shown once.
	WPAdminPassword string `json:"wp_admin_password"`
}

// CreateResult tells the UI where to follow progress.
type CreateResult struct {
	Site          model.Site `json:"site"`
	JobID         int64      `json:"job_id"`
	AdminPassword string     `json:"admin_password,omitempty"`
}

// checkSite validates a site against the others.
func (e *Engine) checkSite(ctx context.Context, s *model.Site, isNew bool) error {
	if err := s.Normalize(); err != nil {
		return err
	}
	if !e.PHPInstalled(s.PHPVersion) {
		return fmt.Errorf("PHP %s is not installed on this server; install it on the System page first", s.PHPVersion)
	}
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return err
	}
	mine := map[string]bool{}
	for _, h := range s.Hostnames() {
		mine[h] = true
	}
	for _, o := range sites {
		if o.ID == s.ID {
			if isNew {
				return fmt.Errorf("a site named %s already exists", s.Name)
			}
			continue
		}
		for _, h := range o.Hostnames() {
			if mine[h] {
				return fmt.Errorf("%s is already served by %s", h, o.Name)
			}
		}
	}
	if isNew && !e.Cfg.DryRun {
		if userExists(s.User()) {
			return fmt.Errorf("system user %s already exists; pick another name", s.User())
		}
		if _, err := os.Stat(e.Paths().SiteDir(s.Name)); err == nil {
			return fmt.Errorf("%s already exists; pick another name", e.Paths().SiteDir(s.Name))
		}
	}
	return nil
}

// CreateSite saves a new site and provisions it in the background.
func (e *Engine) CreateSite(ctx context.Context, req CreateRequest, author string) (CreateResult, error) {
	s := req.Site
	st, _ := e.Store.Settings(ctx)
	if s.PHPVersion == "" {
		s.PHPVersion = st.DefaultPHP
	}
	if s.Name == "" {
		s.Name = model.NameFromDomain(s.Domain)
	}
	s.Status = model.StatusProvisioning
	s.StatusError = ""
	s.CreatedAt = time.Now().UTC()
	// Only the request to publish survives; ids come from Halyard.
	s.Halyard = model.HalyardLink{Enabled: s.Halyard.Enabled, VIPID: s.Halyard.VIPID, Exposure: s.Halyard.Exposure, NoDNS: s.Halyard.NoDNS}
	if !validExposure(s.Halyard.Exposure) {
		return CreateResult{}, fmt.Errorf("unknown exposure %q", s.Halyard.Exposure)
	}
	s.SFTP.PasswordSet = false
	if s.Type.HasDB() {
		s.DB = &model.Database{Name: "site_" + strings.ToLower(s.Name), User: "site_" + strings.ToLower(s.Name), Password: Password(24)}
	} else {
		s.DB = nil
	}
	var adminPW string
	if s.Type == model.TypeWordPress {
		if s.WP == nil {
			s.WP = &model.WordPress{}
		}
		if s.WP.Title == "" {
			s.WP.Title = s.Domain
		}
		if s.WP.AdminUser == "" {
			s.WP.AdminUser = st.WPAdminUser
		}
		if s.WP.AdminEmail == "" {
			s.WP.AdminEmail = st.WPAdminEmail
		}
		if s.WP.Locale == "" {
			s.WP.Locale = st.WPLocale
		}
		if !strings.Contains(s.WP.AdminEmail, "@") && (s.Import == nil || s.Import.Files == "") {
			return CreateResult{}, errors.New("WordPress needs an admin email (set a default on the Settings page)")
		}
		adminPW = req.WPAdminPassword
		if adminPW == "" {
			adminPW = Password(20)
		}
		if s.Import != nil && s.Import.Files != "" {
			adminPW = "" // an imported site keeps its own users
		}
		if len(adminPW) < 10 && adminPW != "" {
			return CreateResult{}, errors.New("the WordPress admin password must be at least 10 characters")
		}
		s.FrontController = true
	} else {
		s.WP = nil
	}
	if err := e.checkSite(ctx, &s, true); err != nil {
		return CreateResult{}, err
	}
	if err := e.checkImport(&s); err != nil {
		return CreateResult{}, err
	}
	if err := e.Store.SaveSite(ctx, s); err != nil {
		return CreateResult{}, err
	}
	e.Store.AddEvent(ctx, "info", "site", s.Name, fmt.Sprintf("%s created %s site %s", author, s.Type.Label(), s.Domain))
	id, err := e.StartJob(s.Name, "create", "Create "+s.Domain, func(ctx context.Context, logf Logf) error {
		unlock := e.lockSite(s.Name)
		defer unlock()
		err := e.provision(ctx, s, adminPW, logf)
		cur, gerr := e.Store.Site(ctx, s.Name)
		if gerr != nil {
			return err
		}
		if err != nil {
			cur.Status, cur.StatusError = model.StatusError, firstLine(err.Error())
		} else {
			cur.Status, cur.StatusError = model.StatusActive, ""
		}
		_ = e.Store.SaveSite(ctx, cur)
		if err == nil {
			e.syncHalyard(ctx, &cur, logf)
		}
		return err
	})
	return CreateResult{Site: s, JobID: id, AdminPassword: adminPW}, err
}

// provision creates everything a site needs. It is safe to run again.
func (e *Engine) provision(ctx context.Context, s model.Site, adminPW string, logf Logf) error {
	logf("Creating system user %s", s.User())
	if err := e.ensureUser(ctx, s); err != nil {
		return err
	}
	logf("Creating %s", e.Paths().SiteDir(s.Name))
	if err := e.ensureDirs(s); err != nil {
		return err
	}
	if s.DB != nil {
		logf("Creating database %s", s.DB.Name)
		if err := e.ensureDB(ctx, *s.DB); err != nil {
			return err
		}
	}
	if err := e.syncSFTP(ctx, s); err != nil {
		logf("SFTP: %v", err)
	}
	logf("Configuring PHP %s and nginx", s.PHPVersion)
	if err := e.Apply(ctx); err != nil {
		return err
	}
	switch {
	case s.Import != nil:
		if err := e.runImport(ctx, s, logf); err != nil {
			return err
		}
		e.finishImport(ctx, s.Name)
	case s.Type == model.TypeWordPress:
		if err := e.installWordPress(ctx, s, adminPW, logf); err != nil {
			return err
		}
	default:
		if err := e.writePlaceholder(s); err != nil {
			return err
		}
	}
	// Installers can change the document root's mode; put the layout back.
	if err := e.FixPermissions(ctx, s); err != nil {
		return err
	}
	logf("%s is live at %s", s.Name, s.URL())
	return nil
}

// RetryProvision runs provisioning again for a site that failed to create.
func (e *Engine) RetryProvision(ctx context.Context, name string) (int64, string, error) {
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return 0, "", err
	}
	if s.Status != model.StatusError {
		return 0, "", errors.New("only a site that failed to create can be retried")
	}
	adminPW := ""
	if s.Type == model.TypeWordPress {
		adminPW = Password(20)
	}
	id, err := e.StartJob(s.Name, "create", "Retry "+s.Domain, func(ctx context.Context, logf Logf) error {
		unlock := e.lockSite(s.Name)
		defer unlock()
		if s.Type == model.TypeWordPress && !e.Cfg.DryRun {
			// Start WordPress from scratch: the earlier attempt may have stopped half way.
			_ = os.RemoveAll(e.Paths().SiteDir(s.Name) + "/public")
		}
		err := e.provision(ctx, s, adminPW, logf)
		cur, _ := e.Store.Site(ctx, s.Name)
		if err != nil {
			cur.Status, cur.StatusError = model.StatusError, firstLine(err.Error())
		} else {
			cur.Status, cur.StatusError = model.StatusActive, ""
		}
		_ = e.Store.SaveSite(ctx, cur)
		if err == nil {
			e.syncHalyard(ctx, &cur, logf)
		}
		return err
	})
	return id, adminPW, err
}

// UpdateSite saves changed settings and applies them. Changing the domain or
// HTTPS of a WordPress site also rewrites its URLs.
func (e *Engine) UpdateSite(ctx context.Context, in model.Site, replaceURLs bool, author string) (model.Site, int64, error) {
	unlock := e.lockSite(in.ID)
	defer unlock()
	cur, err := e.Store.Site(ctx, in.ID)
	if err != nil {
		return cur, 0, err
	}
	if cur.Status == model.StatusProvisioning || cur.Status == model.StatusDeleting {
		return cur, 0, fmt.Errorf("%s is busy (%s)", cur.Name, cur.Status)
	}
	next := cur
	next.Domain, next.Aliases, next.PHPVersion, next.DocRoot = in.Domain, in.Aliases, in.PHPVersion, in.DocRoot
	next.FrontController, next.HTTPS, next.Enabled = in.FrontController, in.HTTPS, in.Enabled
	next.RedirectAliases = in.RedirectAliases
	next.UploadMB, next.MemoryMB, next.MaxChildren = in.UploadMB, in.MemoryMB, in.MaxChildren
	next.Backups, next.Notes = in.Backups, in.Notes
	next.SFTP.Enabled, next.SFTP.Keys = in.SFTP.Enabled, in.SFTP.Keys
	if next.Type == model.TypeWordPress {
		next.FrontController = true
	}
	if err := e.checkSite(ctx, &next, false); err != nil {
		return cur, 0, err
	}
	if err := e.Store.SaveSite(ctx, next); err != nil {
		return cur, 0, err
	}
	if err := e.ensureDirs(next); err != nil {
		_ = e.Store.SaveSite(ctx, cur)
		return cur, 0, err
	}
	if err := e.Apply(ctx); err != nil {
		_ = e.Store.SaveSite(ctx, cur)
		_ = e.Apply(ctx)
		return cur, 0, err
	}
	if err := e.syncSFTP(ctx, next); err != nil {
		return next, 0, err
	}
	e.Store.AddEvent(ctx, "info", "site", next.Name, author+" updated "+next.Domain)
	var jobID int64
	urlChanged := cur.URL() != next.URL()
	hostsChanged := strings.Join(cur.Hostnames(), ",") != strings.Join(next.Hostnames(), ",") || cur.HTTPS != next.HTTPS
	if (urlChanged && replaceURLs && next.Type == model.TypeWordPress && next.Status == model.StatusActive) || (hostsChanged && next.Halyard.Enabled) {
		oldURL := cur.URL()
		jobID, _ = e.StartJob(next.Name, "update", "Update "+next.Domain, func(ctx context.Context, logf Logf) error {
			if urlChanged && replaceURLs && next.Type == model.TypeWordPress {
				if err := e.WPReplaceURL(ctx, next, oldURL, logf); err != nil {
					return err
				}
			}
			if hostsChanged && next.Halyard.Enabled {
				s, _ := e.Store.Site(ctx, next.Name)
				e.syncHalyard(ctx, &s, logf)
			}
			return nil
		})
	}
	return next, jobID, nil
}

// DeleteSite removes a site and everything it owns, optionally keeping a final backup.
func (e *Engine) DeleteSite(ctx context.Context, name string, finalBackup bool, author string) (int64, error) {
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return 0, err
	}
	if s.Status == model.StatusDeleting {
		return 0, errors.New("already deleting")
	}
	prev := s.Status
	s.Status = model.StatusDeleting
	if err := e.Store.SaveSite(ctx, s); err != nil {
		return 0, err
	}
	e.Store.AddEvent(ctx, "warn", "site", s.Name, author+" deleted "+s.Domain)
	return e.StartJob(s.Name, "delete", "Delete "+s.Domain, func(ctx context.Context, logf Logf) error {
		unlock := e.lockSite(s.Name)
		defer unlock()
		if finalBackup && prev != model.StatusProvisioning {
			logf("Taking a final backup")
			if b, err := e.backup(ctx, s, "final", logf); err != nil {
				s.Status = prev
				_ = e.Store.SaveSite(ctx, s)
				return fmt.Errorf("final backup failed, so nothing was deleted: %w", err)
			} else {
				logf("Final backup: %s", b.File)
			}
		}
		_ = e.unlinkHalyard(ctx, &s, logf)
		logf("Removing nginx and PHP config")
		if err := e.Apply(ctx); err != nil {
			s.Status = prev
			_ = e.Store.SaveSite(ctx, s)
			return err
		}
		if s.DB != nil {
			logf("Dropping database %s", s.DB.Name)
			if err := e.dropDB(ctx, *s.DB); err != nil {
				logf("Could not drop the database: %v", err)
			}
		}
		if !e.Cfg.DryRun {
			logf("Deleting files")
			if err := os.RemoveAll(e.Paths().SiteDir(s.Name)); err != nil {
				logf("Could not delete files: %v", err)
			}
			_ = os.RemoveAll(e.Cfg.LogDir + "/" + s.Name)
		}
		logf("Removing system user %s", s.User())
		if err := e.removeUser(ctx, s); err != nil {
			logf("Could not remove the user: %v", err)
		}
		return e.Store.DeleteSite(ctx, s.Name)
	})
}
