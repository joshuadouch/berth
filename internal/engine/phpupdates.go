package engine

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// PHP security and bug-fix releases arrive as package updates from
// packages.sury.org. Berth checks for them nightly and applies them on request.

// PHPPackageUpdate is one upgradable PHP package.
type PHPPackageUpdate struct {
	Package string `json:"package"`
	PHP     string `json:"php"` // 8.3, or "" for shared packages
	From    string `json:"from"`
	To      string `json:"to"`
}

// PHPUpdates is the last check.
type PHPUpdates struct {
	Checked  time.Time          `json:"checked"`
	Updates  []PHPPackageUpdate `json:"updates"`
	Error    string             `json:"error,omitempty"`
	Checking bool               `json:"checking"`
}

type phpUpdateState struct {
	mu sync.Mutex
	PHPUpdates
}

// apt list --upgradable:
// php8.3-fpm/bookworm 8.3.14-1+0~20241122.66+debian12~1.gbp1 amd64 [upgradable from: 8.3.13-1+0~...]
var aptUpgradableRe = regexp.MustCompile(`^([a-z0-9][a-z0-9.+-]*)/\S+\s+(\S+)\s+\S+\s+\[upgradable from: ([^\]]+)\]`)
var phpPkgRe = regexp.MustCompile(`^php([0-9]+\.[0-9]+)?(-|$)`)

func parseUpgradable(out string) []PHPPackageUpdate {
	ups := []PHPPackageUpdate{}
	for _, l := range strings.Split(out, "\n") {
		m := aptUpgradableRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || !phpPkgRe.MatchString(m[1]) {
			continue
		}
		u := PHPPackageUpdate{Package: m[1], To: m[2], From: m[3]}
		if v := phpPkgRe.FindStringSubmatch(m[1]); v != nil {
			u.PHP = v[1]
		}
		ups = append(ups, u)
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].Package < ups[j].Package })
	return ups
}

// PHPUpdateStatus returns the last check without running one.
func (e *Engine) PHPUpdateStatus() PHPUpdates {
	e.phpUp.mu.Lock()
	defer e.phpUp.mu.Unlock()
	out := e.phpUp.PHPUpdates
	out.Updates = append([]PHPPackageUpdate{}, out.Updates...)
	return out
}

// CheckPHPUpdates refreshes the package lists and looks for PHP updates.
func (e *Engine) CheckPHPUpdates(ctx context.Context) PHPUpdates {
	e.phpUp.mu.Lock()
	if e.phpUp.Checking {
		e.phpUp.mu.Unlock()
		return e.PHPUpdateStatus()
	}
	e.phpUp.Checking = true
	e.phpUp.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var ups []PHPPackageUpdate
	var err error
	if _, err = e.Run.Shell(ctx, "DEBIAN_FRONTEND=noninteractive apt-get update -q"); err == nil {
		var out string
		out, err = e.Run.Shell(ctx, "LANG=C apt list --upgradable 2>/dev/null")
		ups = parseUpgradable(out)
	}
	e.phpUp.mu.Lock()
	e.phpUp.Checking = false
	e.phpUp.Checked = time.Now()
	e.phpUp.Updates = ups
	e.phpUp.Error = ""
	if err != nil {
		e.phpUp.Error = firstLine(err.Error())
	}
	e.phpUp.mu.Unlock()
	if err == nil && len(ups) > 0 {
		e.Store.AddEvent(ctx, "info", "php", "", "PHP updates are available on the System page")
	}
	return e.PHPUpdateStatus()
}

// UpdatePHP installs every pending PHP package update, then re-applies the
// site config so each PHP-FPM is running and checked.
func (e *Engine) UpdatePHP(ctx context.Context, logf Logf) error {
	st := e.CheckPHPUpdates(ctx)
	if st.Error != "" {
		return errors.New(st.Error)
	}
	if len(st.Updates) == 0 {
		logf("PHP is up to date")
		return nil
	}
	var pkgs []string
	for _, u := range st.Updates {
		logf("%s %s → %s", u.Package, u.From, u.To)
		pkgs = append(pkgs, u.Package)
	}
	out, err := e.Run.Shell(ctx, "DEBIAN_FRONTEND=noninteractive apt-get install -y -q --only-upgrade "+
		"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "+strings.Join(pkgs, " "))
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(l, "Setting up") || strings.HasPrefix(l, "E:") {
			logf("  %s", l)
		}
	}
	if err != nil {
		return err
	}
	logf("Checking and reloading PHP-FPM")
	if err := e.Apply(ctx); err != nil {
		return err
	}
	e.Store.AddEvent(ctx, "info", "php", "", "Installed PHP updates: "+strings.Join(pkgs, ", "))
	e.CheckPHPUpdates(ctx)
	return nil
}
