package engine

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/model"
)

type usageCache struct {
	mu    sync.Mutex
	files map[string]int64
	dbs   map[string]int64
	at    time.Time
}

// Usage is disk use per site.
type Usage struct {
	Files int64 `json:"files"`
	DB    int64 `json:"db"`
}

// SiteUsage returns the last measured disk use of each site.
func (e *Engine) SiteUsage() map[string]Usage {
	e.usage.mu.Lock()
	defer e.usage.mu.Unlock()
	out := map[string]Usage{}
	for k, v := range e.usage.files {
		u := out[k]
		u.Files = v
		out[k] = u
	}
	for k, v := range e.usage.dbs {
		u := out[k]
		u.DB = v
		out[k] = u
	}
	return out
}

func (e *Engine) measureUsage(ctx context.Context) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	files := map[string]int64{}
	for _, s := range sites {
		out, err := exec.CommandContext(ctx, "du", "-sb", e.Paths().SiteDir(s.Name)).Output()
		if err != nil {
			continue
		}
		if f := strings.Fields(string(out)); len(f) > 0 {
			files[s.Name], _ = strconv.ParseInt(f[0], 10, 64)
		}
	}
	dbs := e.DBSizes(ctx)
	e.usage.mu.Lock()
	e.usage.files, e.usage.dbs, e.usage.at = files, dbs, time.Now()
	e.usage.mu.Unlock()
}

// Loop runs scheduled work: WP-Cron every five minutes, nightly backups and
// disk usage. It returns when ctx ends.
func (e *Engine) Loop(ctx context.Context) {
	go e.measureUsage(ctx)
	go e.fixWebModes(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	lastCron := time.Time{}
	lastUsage := time.Now()
	// PHP updates: check soon after start, then once a day.
	lastPHP := time.Now().Add(-23 * time.Hour)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastCron) >= 5*time.Minute {
				lastCron = now
				go e.RunWPCron(ctx)
			}
			if now.Sub(lastUsage) >= 30*time.Minute {
				lastUsage = now
				go e.measureUsage(ctx)
				go e.cleanImports(ctx)
			}
			e.maybeNightly(ctx, now)
			if now.Sub(lastPHP) >= 24*time.Hour && !e.Cfg.DryRun {
				lastPHP = now
				go e.CheckPHPUpdates(ctx)
			}
		}
	}
}

func (e *Engine) maybeNightly(ctx context.Context, now time.Time) {
	st, err := e.Store.Settings(ctx)
	if err != nil || !st.BackupEnabled || now.Format("15:04") != st.BackupTime {
		return
	}
	var last string
	_ = e.Store.GetKV(ctx, "nightly_last", &last)
	today := now.Format("2006-01-02")
	if last == today {
		return
	}
	_ = e.Store.SetKV(ctx, "nightly_last", today)
	_, _ = e.StartJob("", "backup", "Nightly backups", func(ctx context.Context, logf Logf) error {
		return e.NightlyBackups(ctx, logf)
	})
}

// NightlyBackups backs up every site that has backups on, then prunes.
func (e *Engine) NightlyBackups(ctx context.Context, logf Logf) error {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return err
	}
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, s := range sites {
		if !s.Backups || s.Status != model.StatusActive {
			continue
		}
		logf("— %s", s.Name)
		if _, err := e.Backup(ctx, s, logf); err != nil {
			logf("%s failed: %v", s.Name, err)
			failed = append(failed, s.Name)
			continue
		}
		e.Prune(ctx, s.Name, st.BackupKeep)
		e.pruneRemote(ctx, s.Name, logf)
	}
	if len(failed) > 0 {
		return &multiErr{failed}
	}
	return nil
}

type multiErr struct{ sites []string }

func (m *multiErr) Error() string { return "backups failed for " + strings.Join(m.sites, ", ") }

// Service is a system service and whether it runs.
type Service struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Active string `json:"active"` // active, inactive, failed, unknown
}

// Services reports the state of nginx, MariaDB and each PHP-FPM.
func (e *Engine) Services(ctx context.Context) []Service {
	svcs := []Service{{Name: "nginx", Label: "nginx"}, {Name: "mariadb", Label: "MariaDB"}}
	for _, v := range e.InstalledPHP() {
		svcs = append(svcs, Service{Name: config.V("php{v}-fpm", v), Label: "PHP " + v})
	}
	svcs = append(svcs, Service{Name: "ssh", Label: "SFTP (ssh)"})
	for i := range svcs {
		out, _ := exec.CommandContext(ctx, "systemctl", "is-active", svcs[i].Name).Output()
		svcs[i].Active = strings.TrimSpace(string(out))
		if svcs[i].Active == "" {
			svcs[i].Active = "unknown"
		}
	}
	return svcs
}

// Disk is free space on a filesystem.
type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Disks reports space for the sites and backup folders.
func (e *Engine) Disks(ctx context.Context) []Disk {
	st, _ := e.Store.Settings(ctx)
	var out []Disk
	seen := map[uint64]bool{}
	for _, p := range []string{e.Cfg.SitesDir, st.BackupDir} {
		var fs syscall.Statfs_t
		if _, err := os.Stat(p); err != nil || syscall.Statfs(p, &fs) != nil {
			continue
		}
		key := uint64(fs.Fsid.X__val[0])<<32 | uint64(uint32(fs.Fsid.X__val[1]))
		if seen[key] && key != 0 {
			continue
		}
		seen[key] = true
		out = append(out, Disk{Path: p, Total: fs.Blocks * uint64(fs.Bsize), Free: fs.Bavail * uint64(fs.Bsize)})
	}
	return out
}
