package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/sys"
)

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// GitHubAPI is overridden in tests.
var GitHubAPI = "https://api.github.com"

type sysCmd = sys.Cmd

type upgradeState struct {
	mu        sync.Mutex
	latest    string
	latestURL string
	err       error
	at        time.Time
}

// UpdateInfo describes the running and latest versions.
type UpdateInfo struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest"`
	LatestURL  string    `json:"latest_url"`
	CheckError string    `json:"check_error,omitempty"`
	Checked    time.Time `json:"checked"`
}

// CheckUpdate returns the newest release (cached for ten minutes).
func (e *Engine) CheckUpdate(ctx context.Context, force bool) UpdateInfo {
	u := &e.upgrade
	u.mu.Lock()
	defer u.mu.Unlock()
	if force || time.Since(u.at) > 10*time.Minute {
		u.latest, u.latestURL, u.err = latestRelease(ctx, e.Cfg.UpdateRepo)
		u.at = time.Now()
	}
	info := UpdateInfo{Current: e.Version, Latest: u.latest, LatestURL: u.latestURL, Checked: u.at}
	if u.err != nil {
		info.CheckError = u.err.Error()
	}
	return info
}

func latestRelease(ctx context.Context, repo string) (string, string, error) {
	if !repoRe.MatchString(repo) {
		return "", "", errors.New("update_repo is not set to owner/name")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", GitHubAPI+"/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "berth")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404:
		return "", "", fmt.Errorf("no published release found for %s (the repository must be public and have a release)", repo)
	case (resp.StatusCode == 403 || resp.StatusCode == 429) && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return "", "", errors.New("GitHub's rate limit for this address is used up; try again within the hour")
	case resp.StatusCode != 200:
		return "", "", fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	var r struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", "", err
	}
	return r.TagName, r.HTMLURL, nil
}

var tagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// StartSelfUpdate runs the installer in its own systemd unit, so it survives
// Berth restarting. Sites keep serving throughout.
func (e *Engine) StartSelfUpdate(target string) error {
	if target != "" && !tagRe.MatchString(target) {
		return errors.New("invalid version")
	}
	if !repoRe.MatchString(e.Cfg.UpdateRepo) {
		return errors.New("update_repo is not set to owner/name")
	}
	if e.Cfg.DryRun {
		return nil
	}
	unit := fmt.Sprintf("berth-update-%d", time.Now().Unix())
	args := []string{"--unit=" + unit, "--collect", "--setenv=BERTH_REPO=" + e.Cfg.UpdateRepo,
		"--setenv=LANG=C.UTF-8", "--setenv=LC_ALL=C.UTF-8"}
	if target != "" {
		args = append(args, "--setenv=BERTH_VERSION="+target)
	}
	script := `bash -c "$(curl -fsSL --retry 3 --connect-timeout 20 "https://raw.githubusercontent.com/${BERTH_REPO}/main/install/berth-install.sh")"`
	args = append(args, "/bin/bash", "-c", script)
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("could not start the installer: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallPHP installs a PHP version from packages.sury.org.
func (e *Engine) InstallPHP(ctx context.Context, v string, logf Logf) error {
	if !model.SupportedPHP(v) {
		return fmt.Errorf("PHP %s is not supported", v)
	}
	logf("Installing PHP %s and the extensions sites need", v)
	out, err := e.Run.Exec(ctx, sysCmdShell(config.V(e.Cfg.PHP.InstallCmd, v)))
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(l, "Setting up") || strings.HasPrefix(l, "E:") {
			logf("  %s", l)
		}
	}
	if err != nil {
		return err
	}
	return nil
}

func sysCmdShell(line string) sysCmd {
	return sysCmd{Name: "/bin/sh", Args: []string{"-c", "DEBIAN_FRONTEND=noninteractive " + line}}
}
