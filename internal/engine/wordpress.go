package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/sys"
)

// wp runs WP-CLI as the site's user with the site's PHP version.
func (e *Engine) wp(ctx context.Context, s model.Site, stdin string, args ...string) (string, error) {
	dir := e.Paths().SiteDir(s.Name)
	full := append([]string{"-u", s.User(), "--", config.V(e.Cfg.PHP.CLI, s.PHPVersion),
		"-d", "memory_limit=512M", e.Cfg.WPCLI, "--path=" + filepath.Join(dir, "public", s.DocRoot)}, args...)
	c := sys.Cmd{Name: "runuser", Args: full, Dir: filepath.Join(dir, "tmp"), Env: []string{
		"HOME=" + filepath.Join(dir, "tmp"),
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"WP_CLI_CACHE_DIR=" + filepath.Join(dir, "tmp", ".wp-cli-cache"),
		"LANG=C.UTF-8",
	}}
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	return e.Run.Exec(ctx, c)
}

// wpExtraPHP is added to wp-config.php. HTTPS and the visitor's address
// already reach PHP correctly through nginx (see render.fastcgiConf), so no
// proxy handling is needed here.
const wpExtraPHP = `/* Added by Berth. */
define( 'FS_METHOD', 'direct' );
/* WP-Cron runs every five minutes from Berth instead of on page views. */
define( 'DISABLE_WP_CRON', true );
`

// installWordPress downloads, configures and installs WordPress.
func (e *Engine) installWordPress(ctx context.Context, s model.Site, adminPassword string, logf Logf) error {
	if s.WP == nil || s.DB == nil {
		return fmt.Errorf("site has no WordPress or database settings")
	}
	if e.Cfg.DryRun {
		return nil
	}
	locale := s.WP.Locale
	if locale == "" {
		locale = "en_US"
	}
	logf("Downloading WordPress (%s)", locale)
	if out, err := e.wp(ctx, s, "", "core", "download", "--locale="+locale); err != nil {
		// Some locales have no package; fall back to English.
		if locale == "en_US" || !strings.Contains(out, "locale") {
			return err
		}
		logf("No %s package; downloading English and adding the language after", locale)
		if _, err := e.wp(ctx, s, "", "core", "download"); err != nil {
			return err
		}
	}
	logf("Writing wp-config.php")
	if _, err := e.wp(ctx, s, wpExtraPHP, "config", "create",
		"--dbname="+s.DB.Name, "--dbuser="+s.DB.User, "--dbpass="+s.DB.Password, "--dbhost=localhost",
		"--dbcharset=utf8mb4", "--locale="+locale, "--extra-php"); err != nil {
		return err
	}
	logf("Installing WordPress for %s", s.URL())
	if _, err := e.wp(ctx, s, "", "core", "install", "--url="+s.URL(), "--title="+s.WP.Title,
		"--admin_user="+s.WP.AdminUser, "--admin_password="+adminPassword, "--admin_email="+s.WP.AdminEmail,
		"--skip-email"); err != nil {
		return err
	}
	logf("Removing sample plugins and unused themes")
	_, _ = e.wp(ctx, s, "", "plugin", "delete", "hello", "akismet")
	_, _ = e.wp(ctx, s, "", "theme", "delete", "--all")
	if locale != "en_US" {
		if _, err := e.wp(ctx, s, "", "language", "core", "install", "--activate", locale); err != nil {
			logf("Could not add language %s: %v", locale, err)
		}
	}
	logf("Setting pretty permalinks")
	if _, err := e.wp(ctx, s, "", "rewrite", "structure", "/%postname%/"); err != nil {
		logf("Could not set permalinks: %v", err)
	}
	return nil
}

// WPInfo is what the site page shows about WordPress.
type WPInfo struct {
	Version       string     `json:"version"`
	CoreUpdate    string     `json:"core_update"`
	Plugins       []WPPlugin `json:"plugins"`
	Themes        []WPPlugin `json:"themes"`
	SiteURL       string     `json:"siteurl"`
	Error         string     `json:"error,omitempty"`
	PluginUpdates int        `json:"plugin_updates"`
	ThemeUpdates  int        `json:"theme_updates"`
}

// WPPlugin is a plugin or theme.
type WPPlugin struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version string `json:"version"`
	Update  string `json:"update"`
	Title   string `json:"title"`
}

// WordPressInfo asks WP-CLI for versions and updates.
func (e *Engine) WordPressInfo(ctx context.Context, s model.Site) WPInfo {
	var info WPInfo
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	v, err := e.wp(ctx, s, "", "core", "version")
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Version = strings.TrimSpace(v)
	if out, err := e.wp(ctx, s, "", "core", "check-update", "--field=version", "--format=csv"); err == nil {
		lines := strings.Fields(out)
		for _, l := range lines {
			if l != "version" && !strings.HasPrefix(l, "Success") {
				info.CoreUpdate = l
				break
			}
		}
	}
	if out, err := e.wp(ctx, s, "", "option", "get", "siteurl"); err == nil {
		info.SiteURL = strings.TrimSpace(out)
	}
	list := func(kind string) []WPPlugin {
		out, err := e.wp(ctx, s, "", kind, "list", "--fields=name,status,version,update_version,title", "--format=json")
		if err != nil {
			return nil
		}
		var raw []struct {
			Name, Status, Version, Title string
			Update                       string `json:"update_version"`
		}
		if i := strings.Index(out, "["); i >= 0 {
			_ = json.Unmarshal([]byte(out[i:]), &raw)
		}
		res := []WPPlugin{}
		for _, r := range raw {
			res = append(res, WPPlugin{Name: r.Name, Status: r.Status, Version: r.Version, Update: r.Update, Title: r.Title})
		}
		return res
	}
	info.Plugins = list("plugin")
	info.Themes = list("theme")
	for _, p := range info.Plugins {
		if p.Update != "" {
			info.PluginUpdates++
		}
	}
	for _, t := range info.Themes {
		if t.Update != "" {
			info.ThemeUpdates++
		}
	}
	return info
}

// UpdateWordPress updates core, plugins, themes and translations.
func (e *Engine) UpdateWordPress(ctx context.Context, s model.Site, logf Logf) error {
	steps := [][]string{
		{"core", "update"},
		{"core", "update-db"},
		{"plugin", "update", "--all"},
		{"theme", "update", "--all"},
		{"language", "core", "update"},
	}
	for _, st := range steps {
		logf("wp %s", strings.Join(st, " "))
		out, err := e.wp(ctx, s, "", st...)
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				logf("  %s", l)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ResetWPAdmin sets a new password for a WordPress user (the installer's admin by default).
func (e *Engine) ResetWPAdmin(ctx context.Context, s model.Site, login string) (string, error) {
	if login == "" && s.WP != nil {
		login = s.WP.AdminUser
	}
	if login == "" || strings.HasPrefix(login, "-") {
		return "", fmt.Errorf("which WordPress user?")
	}
	pw := Password(20)
	_, err := e.wp(ctx, s, "", "user", "update", login, "--user_pass="+pw, "--skip-email")
	return pw, err
}

// WPReplaceURL rewrites the site address in the database after a domain or
// HTTPS change.
func (e *Engine) WPReplaceURL(ctx context.Context, s model.Site, oldURL string, logf Logf) error {
	newURL := s.URL()
	if oldURL == "" || oldURL == newURL {
		return nil
	}
	logf("Replacing %s with %s in the database", oldURL, newURL)
	out, err := e.wp(ctx, s, "", "search-replace", oldURL, newURL, "--all-tables-with-prefix", "--skip-columns=guid", "--precise")
	if err != nil {
		return err
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) > 0 {
		logf("  %s", lines[len(lines)-1])
	}
	for _, opt := range []string{"home", "siteurl"} {
		if _, err := e.wp(ctx, s, "", "option", "update", opt, newURL); err != nil {
			return err
		}
	}
	return nil
}

// WPSetDBPassword updates wp-config.php after the database password changes.
func (e *Engine) WPSetDBPassword(ctx context.Context, s model.Site) error {
	_, err := e.wp(ctx, s, "", "config", "set", "DB_PASSWORD", s.DB.Password)
	return err
}

// RunWPCron runs due WP-Cron events on every WordPress site.
func (e *Engine) RunWPCron(ctx context.Context) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	for _, s := range sites {
		if s.Type != model.TypeWordPress || s.Status != model.StatusActive || !s.Enabled {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		if _, err := e.wp(cctx, s, "", "cron", "event", "run", "--due-now", "--quiet"); err != nil {
			e.Log.Warn("wp-cron", "site", s.Name, "err", err)
		}
		cancel()
	}
}
