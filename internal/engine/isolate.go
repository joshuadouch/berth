package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/model"
)

// Accounts whose plan caps CPU, memory or processes run their sites' PHP in
// PHP-FPM masters of their own, one per PHP version, inside a systemd slice
// for the account that carries the caps. Their pools move out of the shared
// masters; sockets keep their names, so nginx does not change.

var accountIDRe = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

const fpmTemplate = `# Written by Berth: PHP-FPM masters of accounts with CPU or memory caps.
[Unit]
Description=PHP-FPM for Berth (%%i)
After=network.target

[Service]
Type=notify
EnvironmentFile=` + "%s" + `/%%i/env
ExecStartPre=/bin/mkdir -p /run/php /run/berth-fpm
ExecStart=/bin/sh -c 'exec "$PHPFPM" --nodaemonize --fpm-config ` + "%s" + `/%%i/php-fpm.conf'
ExecReload=/bin/kill -USR2 $MAINPID
Restart=on-failure
`

// instance names one account's master for one PHP version.
func instance(account, version string) string { return account + "-" + version }

func (e *Engine) instDir(inst string) string { return filepath.Join(e.Cfg.PHP.IsolatedDir, inst) }

func (e *Engine) unitPath(name string) string { return filepath.Join(e.Cfg.SystemdDir, name) }

// isolatedPlans returns the plans that isolate, keyed by account.
func (e *Engine) isolatedPlans(ctx context.Context) map[string]model.Plan {
	out := map[string]model.Plan{}
	if e.Cfg.PHP.IsolatedDir == "" || e.Cfg.SystemdDir == "" {
		return out
	}
	accts, err := e.Store.Accounts(ctx)
	if err != nil {
		return out
	}
	for _, a := range accts {
		if p, ok := e.Store.AccountPlan(ctx, a.ID); ok && p.Isolated() && accountIDRe.MatchString(a.ID) {
			out[a.ID] = p
		}
	}
	return out
}

func sliceName(account string) string { return "berth-acct-" + account + ".slice" }

// isolate rewrites the rendered files so isolated accounts' pools live in
// their own masters, and adds those masters' config and units. It returns
// the instances it made.
func (e *Engine) isolate(ctx context.Context, files map[string]string, sites []model.Site) []string {
	plans := e.isolatedPlans(ctx)
	if len(plans) == 0 {
		return nil
	}
	p := e.Paths()
	insts := map[string]bool{}
	accounts := map[string]bool{}
	for _, s := range sites {
		if _, ok := plans[s.AccountID]; !ok {
			continue
		}
		shared := p.PoolFile(s.PHPVersion, s.Name)
		pool, ok := files[shared]
		if !ok {
			continue
		}
		delete(files, shared)
		inst := instance(s.AccountID, s.PHPVersion)
		files[filepath.Join(e.instDir(inst), filepath.Base(shared))] = pool
		insts[inst] = true
		accounts[s.AccountID] = true
	}
	var out []string
	for inst := range insts {
		out = append(out, inst)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	for _, inst := range out {
		account, version, _ := strings.Cut(inst, "-")
		dir := e.instDir(inst)
		files[filepath.Join(dir, "php-fpm.conf")] = fmt.Sprintf("; Written by Berth for account %s, PHP %s.\n[global]\npid = /run/berth-fpm/%s.pid\nerror_log = %s\nprocess_control_timeout = 10\ninclude = %s/berth-*.conf\n",
			account, version, inst, filepath.Join(e.Cfg.LogDir, "fpm-"+inst+".log"), dir)
		files[filepath.Join(dir, "env")] = "PHPFPM=" + config.V(e.Cfg.PHP.FPMBinary, version) + "\n"
		files[e.unitPath("berth-fpm@"+inst+".service.d/berth.conf")] = "# Written by Berth.\n[Service]\nSlice=" + sliceName(account) + "\n"
	}
	for account := range accounts {
		pl := plans[account]
		var b strings.Builder
		fmt.Fprintf(&b, "# Written by Berth: limits of plan %s for account %s.\n[Unit]\nDescription=Berth account %s\n\n[Slice]\n", pl.Name, account, account)
		if pl.CPUPercent > 0 {
			fmt.Fprintf(&b, "CPUQuota=%d%%\n", pl.CPUPercent)
		}
		if pl.RAMMB > 0 {
			fmt.Fprintf(&b, "MemoryHigh=%dM\nMemoryMax=%dM\n", pl.RAMMB*9/10, pl.RAMMB)
		}
		if pl.MaxProcesses > 0 {
			fmt.Fprintf(&b, "TasksMax=%d\n", pl.MaxProcesses)
		}
		files[e.unitPath(sliceName(account))] = b.String()
	}
	files[e.unitPath("berth-fpm@.service")] = fmt.Sprintf(fpmTemplate, e.Cfg.PHP.IsolatedDir, e.Cfg.PHP.IsolatedDir)
	var wants []string
	for _, inst := range out {
		wants = append(wants, "berth-fpm@"+inst+".service")
	}
	files[e.unitPath("berth-fpm.target")] = "# Written by Berth: starts the isolated PHP-FPM masters at boot.\n[Unit]\nDescription=Berth PHP-FPM masters\nWants=" +
		strings.Join(wants, " ") + "\n\n[Install]\nWantedBy=multi-user.target\n"
	return out
}

// isolatedFiles lists the isolation files Berth owns on disk.
func (e *Engine) isolatedFiles() []string {
	if e.Cfg.PHP.IsolatedDir == "" || e.Cfg.SystemdDir == "" {
		return nil
	}
	var out []string
	for _, pat := range []string{
		filepath.Join(e.Cfg.PHP.IsolatedDir, "*", "php-fpm.conf"),
		filepath.Join(e.Cfg.PHP.IsolatedDir, "*", "env"),
		filepath.Join(e.Cfg.PHP.IsolatedDir, "*", "berth-*.conf"),
		e.unitPath("berth-fpm@.service"),
		e.unitPath("berth-fpm.target"),
		e.unitPath("berth-fpm@*.service.d/berth.conf"),
		e.unitPath("berth-acct-*.slice"),
	} {
		m, _ := filepath.Glob(pat)
		out = append(out, m...)
	}
	return out
}

// runningInstances lists the instances whose master config is on disk.
func (e *Engine) runningInstances() []string {
	if e.Cfg.PHP.IsolatedDir == "" {
		return nil
	}
	m, _ := filepath.Glob(filepath.Join(e.Cfg.PHP.IsolatedDir, "*", "php-fpm.conf"))
	var out []string
	for _, f := range m {
		out = append(out, filepath.Base(filepath.Dir(f)))
	}
	sort.Strings(out)
	return out
}

// instanceOf returns the instance a file belongs to ("" for none).
func (e *Engine) instanceOf(path string) string {
	if e.Cfg.PHP.IsolatedDir != "" {
		if rel, err := filepath.Rel(e.Cfg.PHP.IsolatedDir, path); err == nil && !strings.HasPrefix(rel, "..") && strings.Contains(rel, string(filepath.Separator)) {
			return strings.SplitN(rel, string(filepath.Separator), 2)[0]
		}
	}
	return ""
}

// isUnit reports whether a file is one of Berth's systemd units.
func (e *Engine) isUnit(path string) bool {
	return e.Cfg.SystemdDir != "" && strings.HasPrefix(path, e.Cfg.SystemdDir+string(filepath.Separator))
}
