package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joshuadouch/berth/internal/model"
)

// One-click WordPress login. Berth drops a small must-use plugin into the
// site that trades a single-use token for a login cookie. The token lives in
// a WordPress transient, stored as a SHA-256 hash, for one minute, and is
// deleted the moment it is used.

const wpLoginPlugin = `<?php
/*
 * Plugin Name: Berth one-click login
 * Description: Lets the Berth control panel open WP admin without a password. Managed by Berth; changes are overwritten.
 */
defined('ABSPATH') || exit;
add_action('init', function () {
	if (empty($_GET['berth_login']) || !is_string($_GET['berth_login'])) {
		return;
	}
	$key = 'berth_login_' . hash('sha256', $_GET['berth_login']);
	$login = get_transient($key);
	delete_transient($key);
	nocache_headers();
	$user = $login ? get_user_by('login', $login) : false;
	if (!$user) {
		wp_die('This login link has expired or was already used. Open WP admin from Berth again.', 'Login link expired', array('response' => 403));
	}
	wp_set_current_user($user->ID);
	wp_set_auth_cookie($user->ID, false, is_ssl());
	do_action('wp_login', $user->user_login, $user);
	wp_safe_redirect(admin_url());
	exit;
}, 1);
`

const wpLoginFile = "berth-login.php"

// installWPLoginPlugin writes the must-use plugin as the site's user, inside
// the site's folder only.
func (e *Engine) installWPLoginPlugin(s model.Site) error {
	if e.Cfg.DryRun {
		return nil
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	dir := filepath.Join("public", s.DocRoot, "wp-content", "mu-plugins")
	if err := r.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	e.chownNew(r, s, dir)
	rel := filepath.Join(dir, wpLoginFile)
	if old, err := r.ReadFile(rel); err == nil && string(old) == wpLoginPlugin {
		return nil
	}
	tmp := rel + ".berth-tmp"
	if err := r.WriteFile(tmp, []byte(wpLoginPlugin), 0o644); err != nil {
		return err
	}
	e.chownNew(r, s, tmp)
	return r.Rename(tmp, rel)
}

// wpLoginUser picks who to log in as: the admin Berth created if it still
// exists and is an administrator, otherwise the first administrator.
func (e *Engine) wpLoginUser(ctx context.Context, s model.Site) (string, error) {
	out, err := e.wp(ctx, s, "", "user", "list", "--role=administrator", "--field=user_login", "--orderby=ID", "--order=ASC",
		"--skip-plugins", "--skip-themes")
	if err != nil {
		return "", err
	}
	var admins []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			admins = append(admins, l)
		}
	}
	if len(admins) == 0 {
		return "", fmt.Errorf("this site has no WordPress administrator to log in as")
	}
	if s.WP != nil && slices.Contains(admins, s.WP.AdminUser) {
		return s.WP.AdminUser, nil
	}
	return admins[0], nil
}

// WPLoginURL returns a link that logs into WP admin once, within a minute.
func (e *Engine) WPLoginURL(ctx context.Context, s model.Site) (string, string, error) {
	if e.Cfg.DryRun {
		login := "admin"
		if s.WP != nil && s.WP.AdminUser != "" {
			login = s.WP.AdminUser
		}
		return s.URL() + "/wp-admin/", login, nil
	}
	if err := e.installWPLoginPlugin(s); err != nil {
		return "", "", fmt.Errorf("install the login helper: %w", err)
	}
	login, err := e.wpLoginUser(ctx, s)
	if err != nil {
		return "", "", err
	}
	token := Password(40)
	sum := sha256.Sum256([]byte(token))
	if _, err := e.wp(ctx, s, "", "transient", "set", "berth_login_"+hex.EncodeToString(sum[:]), login, "60",
		"--skip-plugins", "--skip-themes"); err != nil {
		return "", "", err
	}
	return s.URL() + "/wp-login.php?berth_login=" + url.QueryEscape(token), login, nil
}
