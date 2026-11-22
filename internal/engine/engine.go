// Package engine provisions sites and applies nginx and PHP-FPM config.
package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/render"
	"github.com/joshuadouch/berth/internal/store"
	"github.com/joshuadouch/berth/internal/sys"
)

// PluginSwitch reports whether a plugin is on for an account.
type PluginSwitch interface {
	ActiveFor(ctx context.Context, id, account string) bool
}

// Engine owns every change to the machine.
type Engine struct {
	Cfg     *config.Config
	Store   *store.Store
	Log     *slog.Logger
	Run     *sys.Runner
	Version string
	// HalyardHTTP overrides the HTTP client used to call Halyard (tests).
	HalyardHTTP *http.Client
	// S3Transport and S3Insecure point off-site backups at a test server.
	S3Transport http.RoundTripper
	S3Insecure  bool
	// HTTP and LocalBase point outgoing checks at test servers.
	HTTP      *http.Client
	LocalBase string
	// Plugins says which plugins are on for an account. Nil means every
	// plugin is on (tests).
	Plugins PluginSwitch
	// ACMEDirectory, ACMEHTTP and SkipACMEPreflight point certificate
	// requests at a test CA.
	ACMEDirectory     string
	ACMEHTTP          *http.Client
	SkipACMEPreflight bool

	applyMu sync.Mutex
	siteMu  sync.Map // site name -> *sync.Mutex
	usage   usageCache
	upgrade upgradeState
	remote  remoteCache

	healthFixed atomic.Bool
	index       indexCache
	phpUp       phpUpdateState
	vulns       vulnState
	traffic     trafficState
	res         resourceState
}

// New creates an engine.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Engine {
	return &Engine{Cfg: cfg, Store: st, Log: log, Run: &sys.Runner{DryRun: cfg.DryRun, Log: log}}
}

// Paths returns the render paths from the config.
func (e *Engine) Paths() render.Paths {
	return render.Paths{
		SitesDir: e.Cfg.SitesDir, LogDir: e.Cfg.LogDir, NginxConf: e.Cfg.Nginx.ConfDir,
		PoolDir: e.Cfg.PHP.PoolDir, SocketTmpl: e.Cfg.PHP.Socket, IPv6: e.Cfg.Nginx.IPv6,
		ACMEDir: e.Cfg.ACMEDir, CertDir: e.certDir(),
	}
}

func (e *Engine) certDir() string {
	if e.Cfg.CertDir != "" {
		return e.Cfg.CertDir
	}
	return e.Cfg.Path("certs")
}

// lockSite serialises work on one site.
func (e *Engine) lockSite(name string) func() {
	m, _ := e.siteMu.LoadOrStore(name, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// InstalledPHP lists PHP versions whose FPM binary exists.
func (e *Engine) InstalledPHP() []string {
	out := []string{}
	if e.Cfg.DryRun {
		return append([]string{}, model.PHPVersions...)
	}
	for _, v := range model.PHPVersions {
		if _, err := os.Stat(config.V(e.Cfg.PHP.FPMBinary, v)); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// PHPInstalled reports whether a PHP version can run sites.
func (e *Engine) PHPInstalled(v string) bool {
	if e.Cfg.DryRun {
		return true
	}
	for _, x := range e.InstalledPHP() {
		if x == v {
			return true
		}
	}
	return false
}

func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

// renderable returns the sites whose system user exists, so a half-created
// site never breaks the config of the others.
func (e *Engine) renderable(ctx context.Context) ([]model.Site, error) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Site
	for _, s := range sites {
		if s.Status == model.StatusDeleting || s.Status == model.StatusProvisioning && !userExists(s.User()) {
			continue
		}
		if !e.Cfg.DryRun && (!userExists(s.User()) || !e.PHPInstalled(s.PHPVersion)) {
			continue
		}
		// A certificate that is missing on disk (a restored panel, say) would
		// stop nginx from loading: serve plain HTTP until it is issued again.
		if s.ServesTLS() && !e.Cfg.DryRun {
			if _, err := os.Stat(e.Paths().CertFile(s.Name)); err != nil {
				s.TLS.Status = model.TLSPending
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// managedFiles lists the files Berth currently owns on disk.
func (e *Engine) managedFiles() []string {
	p := e.Paths()
	var files []string
	for _, pat := range []string{
		filepath.Join(p.NginxConf, "*.conf"),
		filepath.Join(p.NginxConf, "sites", "*.conf"),
	} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	for _, v := range model.PHPVersions {
		m, _ := filepath.Glob(filepath.Join(config.V(p.PoolDir, v), "berth-*.conf"))
		files = append(files, m...)
	}
	return files
}

func versionOfPool(p render.Paths, path string) string {
	for _, v := range model.PHPVersions {
		if filepath.Dir(path) == config.V(p.PoolDir, v) {
			return v
		}
	}
	return ""
}

// Apply renders every site and makes nginx and PHP-FPM match. A config that
// fails `nginx -t` or `php-fpm -t` is rolled back and nothing is reloaded.
func (e *Engine) Apply(ctx context.Context) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return err
	}
	sites, err := e.renderable(ctx)
	if err != nil {
		return err
	}
	p := e.Paths()
	out, err := render.Render(p, st, sites)
	if err != nil {
		return err
	}
	for _, s := range sites {
		_ = os.MkdirAll(filepath.Join(e.Cfg.LogDir, s.Name), 0o750)
	}
	_ = os.MkdirAll(filepath.Join(p.NginxConf, "sites"), 0o755)

	// Work out the changes, keeping the old content for rollback.
	old := map[string]*string{}
	nginxChanged := false
	fpm := map[string]bool{}
	mark := func(path string) {
		if v := versionOfPool(p, path); v != "" {
			fpm[v] = true
		} else {
			nginxChanged = true
		}
	}
	for path, content := range out.Files {
		cur, err := os.ReadFile(path)
		if err == nil && string(cur) == content {
			continue
		}
		if err == nil {
			s := string(cur)
			old[path] = &s
		} else {
			old[path] = nil
		}
		mark(path)
	}
	for _, path := range e.managedFiles() {
		if _, ok := out.Files[path]; ok {
			continue
		}
		cur, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := string(cur)
		old[path] = &s
		mark(path)
	}
	if len(old) == 0 {
		return nil
	}
	rollback := func() {
		for path, content := range old {
			if content == nil {
				_ = os.Remove(path)
			} else {
				_ = writeFile(path, *content, 0o644)
			}
		}
	}
	for path := range old {
		if content, ok := out.Files[path]; ok {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				rollback()
				return err
			}
			if err := writeFile(path, content, 0o644); err != nil {
				rollback()
				return err
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollback()
			return err
		}
	}
	var versions []string
	for v := range fpm {
		if _, err := os.Stat(config.V(e.Cfg.PHP.FPMBinary, v)); err == nil || e.Cfg.DryRun {
			versions = append(versions, v)
		}
	}
	sort.Strings(versions)
	if nginxChanged {
		if _, err := e.Run.Run(ctx, e.Cfg.Nginx.Binary, "-t", "-q"); err != nil {
			rollback()
			return fmt.Errorf("nginx rejected the new config, so nothing changed: %w", err)
		}
	}
	for _, v := range versions {
		if _, err := e.Run.Run(ctx, config.V(e.Cfg.PHP.FPMBinary, v), "-t", "-y", config.V(e.Cfg.PHP.FPMConfig, v)); err != nil {
			rollback()
			return fmt.Errorf("PHP %s rejected the new config, so nothing changed: %w", v, err)
		}
	}
	// Reload PHP first, so new sockets exist before nginx sends traffic.
	var errs []string
	for _, v := range versions {
		if _, err := e.Run.Shell(ctx, config.V(e.Cfg.PHP.ReloadCmd, v)); err != nil {
			errs = append(errs, fmt.Sprintf("reload PHP %s: %v", v, err))
		}
	}
	if nginxChanged {
		if _, err := e.Run.Shell(ctx, e.Cfg.Nginx.ReloadCmd); err != nil {
			errs = append(errs, fmt.Sprintf("reload nginx: %v", err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	e.Log.Info("applied config", "files", len(old))
	return nil
}

// writeFile writes atomically.
func writeFile(path, content string, mode os.FileMode) error {
	tmp := path + ".berth-tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const pwChars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Password returns a random password of n characters with no ambiguous ones.
func Password(n int) string {
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(pwChars))))
		b[i] = pwChars[x.Int64()]
	}
	return string(b)
}
