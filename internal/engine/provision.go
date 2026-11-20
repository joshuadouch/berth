package engine

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/sys"
)

// Logf receives progress lines from long operations.
type Logf func(format string, a ...any)

// ids returns the uid and gid of a user and of the web server group.
func (e *Engine) ids(name string) (uid, gid, webGID int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, 0, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	webGID = gid
	if g, err := user.LookupGroup(e.Cfg.Nginx.User); err == nil {
		webGID, _ = strconv.Atoi(g.Gid)
	}
	return uid, gid, webGID, nil
}

// ensureUser creates the site's system user. It owns the files and runs PHP.
func (e *Engine) ensureUser(ctx context.Context, s model.Site) error {
	if e.Cfg.DryRun || userExists(s.User()) {
		return nil
	}
	_, err := e.Run.Run(ctx, "useradd", "--system", "--user-group", "--no-create-home",
		"--home-dir", e.Paths().SiteDir(s.Name), "--shell", "/usr/sbin/nologin",
		"--comment", "Berth site "+s.Domain, s.User())
	return err
}

// ensureDirs creates the site layout:
//
//	/srv/sites/<name>            root, 0751 (SFTP chroot; others may only pass through)
//	/srv/sites/<name>/public     site user, group www-data, 0750
//	/srv/sites/<name>/logs       site user, 0750 (PHP error log)
//	/srv/sites/<name>/tmp        site user, 0700
//	/srv/sites/<name>/sessions   site user, 0700
func (e *Engine) ensureDirs(s model.Site) error {
	if e.Cfg.DryRun {
		return nil
	}
	uid, gid, web, err := e.ids(s.User())
	if err != nil {
		return err
	}
	dir := e.Paths().SiteDir(s.Name)
	if err := os.MkdirAll(dir, 0o751); err != nil {
		return err
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o751); err != nil {
		return err
	}
	for _, d := range []struct {
		name string
		gid  int
		mode os.FileMode
	}{{"public", web, 0o750}, {"logs", gid, 0o750}, {"tmp", gid, 0o700}, {"sessions", gid, 0o700}} {
		p := filepath.Join(dir, d.name)
		if err := os.MkdirAll(p, d.mode); err != nil {
			return err
		}
		if err := os.Chown(p, uid, d.gid); err != nil {
			return err
		}
		if err := os.Chmod(p, d.mode); err != nil {
			return err
		}
	}
	if s.DocRoot != "" {
		root := filepath.Join(dir, "public", s.DocRoot)
		if _, err := os.Stat(root); os.IsNotExist(err) {
			if err := os.MkdirAll(root, 0o755); err != nil {
				return err
			}
			_ = chownTree(filepath.Join(dir, "public"), uid, gid)
		}
	}
	return os.MkdirAll(filepath.Join(e.Cfg.LogDir, s.Name), 0o750)
}

// chownTree hands every file under root to uid:gid without following links.
func chownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		return os.Lchown(p, uid, gid)
	})
}

// FixPermissions gives the site user every file again (after copying files
// in as root) and resets the directory layout.
func (e *Engine) FixPermissions(ctx context.Context, s model.Site) error {
	if e.Cfg.DryRun {
		return nil
	}
	if err := e.ensureDirs(s); err != nil {
		return err
	}
	uid, gid, web, err := e.ids(s.User())
	if err != nil {
		return err
	}
	dir := e.Paths().SiteDir(s.Name)
	for _, d := range []string{"public", "logs", "tmp", "sessions"} {
		if err := chownTree(filepath.Join(dir, d), uid, gid); err != nil {
			return err
		}
	}
	return os.Chown(filepath.Join(dir, "public"), uid, web)
}

// placeholder is the first page of a new PHP site.
const placeholder = `<?php
// Placeholder page created by Berth. Replace it with your site.
?><!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title><?= htmlspecialchars($_SERVER['HTTP_HOST'] ?? 'New site') ?></title>
<body style="font:16px/1.5 system-ui;margin:18vh auto;max-width:32em;padding:0 16px;color:#1f2937">
<h1 style="font-size:22px"><?= htmlspecialchars($_SERVER['HTTP_HOST'] ?? '') ?> is ready</h1>
<p>Upload your site over SFTP into <code>public</code>, or copy it in and press Fix permissions in Berth.</p>
<p style="color:#6b7280;font-size:13px">PHP <?= PHP_VERSION ?> · <?= ($_SERVER['HTTPS'] ?? '') === 'on' ? 'HTTPS' : 'HTTP' ?> · visitor <?= htmlspecialchars($_SERVER['REMOTE_ADDR'] ?? '') ?></p>
`

// writePlaceholder adds index.php to an empty document root.
func (e *Engine) writePlaceholder(s model.Site) error {
	if e.Cfg.DryRun {
		return nil
	}
	root := filepath.Join(e.Paths().SiteDir(s.Name), "public", s.DocRoot)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) > 0 {
		return err
	}
	uid, gid, _, err := e.ids(s.User())
	if err != nil {
		return err
	}
	p := filepath.Join(root, "index.php")
	if err := os.WriteFile(p, []byte(placeholder), 0o644); err != nil {
		return err
	}
	return os.Chown(p, uid, gid)
}

// Database ---------------------------------------------------------------------

func (e *Engine) sql(ctx context.Context, stmt string) (string, error) {
	return e.Run.Exec(ctx, sys.Cmd{Name: e.Cfg.MySQL.Client, Args: []string{"--batch", "--skip-column-names"}, Stdin: strings.NewReader(stmt)})
}

// ensureDB creates the database and user and (re)sets the password.
func (e *Engine) ensureDB(ctx context.Context, db model.Database) error {
	if !model.ValidName(strings.TrimPrefix(db.Name, "site_")) || db.Name != db.User || strings.ContainsAny(db.Password, `'\"`+"`") {
		return fmt.Errorf("invalid database settings")
	}
	_, err := e.sql(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"+
		"CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\n"+
		"ALTER USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\n"+
		"GRANT ALL PRIVILEGES ON `%[1]s`.* TO '%[1]s'@'localhost';\n"+
		"FLUSH PRIVILEGES;\n", db.Name, db.Password))
	return err
}

// dropDB removes the database and its user.
func (e *Engine) dropDB(ctx context.Context, db model.Database) error {
	if !model.ValidName(strings.TrimPrefix(db.Name, "site_")) {
		return fmt.Errorf("invalid database name")
	}
	_, err := e.sql(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`;\nDROP USER IF EXISTS '%[1]s'@'localhost';\n", db.Name))
	return err
}

// DBSizes returns the size in bytes of every site database.
func (e *Engine) DBSizes(ctx context.Context) map[string]int64 {
	out := map[string]int64{}
	res, err := e.sql(ctx, "SELECT table_schema, COALESCE(SUM(data_length+index_length),0) FROM information_schema.tables WHERE table_schema LIKE 'site\\_%' GROUP BY table_schema;")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(res, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			out[strings.TrimPrefix(f[0], "site_")] = n
		}
	}
	return out
}

// SFTP ---------------------------------------------------------------------------

// syncSFTP puts the site user in or out of the SFTP group and writes its keys.
func (e *Engine) syncSFTP(ctx context.Context, s model.Site) error {
	if e.Cfg.DryRun || !e.Cfg.SFTP.Enabled {
		return nil
	}
	keyFile := filepath.Join(e.Cfg.SFTP.KeysDir, s.User())
	if !s.SFTP.Enabled {
		_ = os.Remove(keyFile)
		_, _ = e.Run.Run(ctx, "gpasswd", "-d", s.User(), e.Cfg.SFTP.Group)
		_, _ = e.Run.Run(ctx, "usermod", "--lock", s.User())
		return nil
	}
	if _, err := e.Run.Run(ctx, "usermod", "-a", "-G", e.Cfg.SFTP.Group, s.User()); err != nil {
		return fmt.Errorf("add %s to %s (is SFTP set up? run berth-update): %w", s.User(), e.Cfg.SFTP.Group, err)
	}
	if err := os.MkdirAll(e.Cfg.SFTP.KeysDir, 0o755); err != nil {
		return err
	}
	if len(s.SFTP.Keys) == 0 {
		_ = os.Remove(keyFile)
	} else if err := writeFile(keyFile, strings.Join(s.SFTP.Keys, "\n")+"\n", 0o644); err != nil {
		return err
	}
	if s.SFTP.PasswordSet {
		_, _ = e.Run.Run(ctx, "usermod", "--unlock", s.User())
	}
	return nil
}

// SetSFTPPassword sets the site user's password for SFTP.
func (e *Engine) SetSFTPPassword(ctx context.Context, s model.Site, pw string) error {
	if len(pw) < 12 || strings.ContainsAny(pw, ":\n\r") {
		return fmt.Errorf("password must be at least 12 characters, without colons")
	}
	if _, err := e.Run.Exec(ctx, sys.Cmd{Name: "chpasswd", Stdin: strings.NewReader(s.User() + ":" + pw + "\n")}); err != nil {
		return err
	}
	_, err := e.Run.Run(ctx, "usermod", "--unlock", s.User())
	return err
}

// removeUser deletes the site's system user and SFTP keys.
func (e *Engine) removeUser(ctx context.Context, s model.Site) error {
	if e.Cfg.DryRun || !userExists(s.User()) {
		return nil
	}
	_ = os.Remove(filepath.Join(e.Cfg.SFTP.KeysDir, s.User()))
	_, _ = e.Run.Run(ctx, "pkill", "-KILL", "-u", s.User())
	_, err := e.Run.Run(ctx, "userdel", s.User())
	return err
}

// SetDBPassword changes the database password and, for WordPress, wp-config.php.
func (e *Engine) SetDBPassword(ctx context.Context, s model.Site) error {
	unlock := e.lockSite(s.Name)
	defer unlock()
	if err := e.ensureDB(ctx, *s.DB); err != nil {
		return err
	}
	if s.Type == model.TypeWordPress {
		if err := e.WPSetDBPassword(ctx, s); err != nil {
			return err
		}
	}
	return e.Store.SaveSite(ctx, s)
}
