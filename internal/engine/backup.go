package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
	"github.com/joshuadouch/berth/internal/sys"
)

// Backups are single .tar.gz files:
//
//	site.json       the site's settings (including the database password)
//	database.sql    mariadb-dump of the site's database, when it has one
//	files/...       the site directory without tmp and sessions
//
// They live in <backup_dir>/<site>/<site>-YYYYMMDD-HHMMSS.tar.gz.

var backupFileRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,23}-[0-9]{8}-[0-9]{6}(-[a-z]+)?\.tar\.gz$`)

// ValidBackupName reports whether f looks like a Berth backup file name.
func ValidBackupName(f string) bool { return backupFileRe.MatchString(f) }

func (e *Engine) backupDir(ctx context.Context, site string) (string, error) {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(st.BackupDir, site), nil
}

// BackupPath returns the full path of a backup file, checking its name.
func (e *Engine) BackupPath(ctx context.Context, site, file string) (string, error) {
	if !model.ValidName(site) || !ValidBackupName(file) || !strings.HasPrefix(file, site+"-") {
		return "", errors.New("invalid backup name")
	}
	dir, err := e.backupDir(ctx, site)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, file)
	if _, err := os.Stat(p); err != nil {
		return "", store.ErrNotFound
	}
	return p, nil
}

// Backups lists a site's backups (or every site's when site is empty).
func (e *Engine) Backups(ctx context.Context, site string) ([]model.Backup, error) {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	var dirs []string
	if site != "" {
		dirs = []string{filepath.Join(st.BackupDir, site)}
	} else {
		ents, _ := os.ReadDir(st.BackupDir)
		for _, d := range ents {
			if d.IsDir() && model.ValidName(d.Name()) {
				dirs = append(dirs, filepath.Join(st.BackupDir, d.Name()))
			}
		}
	}
	out := []model.Backup{}
	for _, d := range dirs {
		ents, _ := os.ReadDir(d)
		name := filepath.Base(d)
		for _, f := range ents {
			if f.IsDir() || !ValidBackupName(f.Name()) || !strings.HasPrefix(f.Name(), name+"-") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			out = append(out, model.Backup{Site: name, File: f.Name(), Size: info.Size(), Time: backupTime(f.Name(), info.ModTime()), Local: true})
		}
	}
	if remote, err := e.remoteBackups(ctx); err == nil {
		idx := map[string]int{}
		for i, b := range out {
			idx[b.File] = i
		}
		for _, b := range remote {
			if site != "" && b.Site != site {
				continue
			}
			if i, ok := idx[b.File]; ok {
				out[i].Remote = true
				continue
			}
			out = append(out, b)
		}
	}
	store.SortBackups(out)
	return out, nil
}

func backupTime(name string, fallback time.Time) time.Time {
	m := regexp.MustCompile(`-([0-9]{8}-[0-9]{6})`).FindStringSubmatch(name)
	if m == nil {
		return fallback
	}
	t, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
	if err != nil {
		return fallback
	}
	return t
}

// Backup takes a backup now.
func (e *Engine) Backup(ctx context.Context, s model.Site, logf Logf) (model.Backup, error) {
	unlock := e.lockSite(s.Name)
	defer unlock()
	return e.backup(ctx, s, "", logf)
}

// backup writes an archive; a suffix ("final") marks backups pruning keeps.
func (e *Engine) backup(ctx context.Context, s model.Site, suffix string, logf Logf) (model.Backup, error) {
	dir, err := e.backupDir(ctx, s.Name)
	if err != nil {
		return model.Backup{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return model.Backup{}, err
	}
	now := time.Now()
	name := fmt.Sprintf("%s-%s", s.Name, now.Format("20060102-150405"))
	if suffix != "" {
		name += "-" + suffix
	}
	name += ".tar.gz"
	final := filepath.Join(dir, name)
	tmp := final + ".partial"
	defer os.Remove(tmp)

	var dump string
	if s.DB != nil {
		logf("Dumping database %s", s.DB.Name)
		f, err := os.CreateTemp(dir, ".dump-*.sql")
		if err != nil {
			return model.Backup{}, err
		}
		dump = f.Name()
		defer os.Remove(dump)
		_, err = e.Run.Exec(ctx, sys.Cmd{Name: e.Cfg.MySQL.Dump, Stdout: f, Args: []string{
			"--single-transaction", "--quick", "--routines", "--triggers", "--events",
			"--default-character-set=utf8mb4", "--add-drop-table", s.DB.Name}})
		f.Close()
		if err != nil {
			return model.Backup{}, fmt.Errorf("database dump: %w", err)
		}
	}

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return model.Backup{}, err
	}
	defer out.Close()
	gz, _ := gzip.NewWriterLevel(out, gzip.DefaultCompression)
	tw := tar.NewWriter(gz)

	meta, _ := json.MarshalIndent(struct {
		Format  int        `json:"format"`
		Created time.Time  `json:"created"`
		Berth   string     `json:"berth"`
		Site    model.Site `json:"site"`
	}{1, now.UTC(), e.Version, s}, "", "  ")
	if err := addBytes(tw, "site.json", meta, now); err != nil {
		return model.Backup{}, err
	}
	if dump != "" {
		if err := addFile(tw, dump, "database.sql"); err != nil {
			return model.Backup{}, err
		}
	}
	siteDir := e.Paths().SiteDir(s.Name)
	logf("Archiving %s", siteDir)
	files := 0
	err = filepath.WalkDir(siteDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		rel, _ := filepath.Rel(siteDir, p)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if top == "tmp" || top == "sessions" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil
		}
		hdr.Name = "files/" + filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname = "", ""
		if !info.Mode().IsRegular() {
			return tw.WriteHeader(hdr)
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// Copy exactly the size in the header even if the file changes underneath.
		n, err := io.Copy(tw, io.LimitReader(f, hdr.Size))
		if err != nil {
			return err
		}
		if n < hdr.Size {
			if _, err := io.CopyN(tw, zeroReader{}, hdr.Size-n); err != nil {
				return err
			}
		}
		files++
		return nil
	})
	if err != nil {
		return model.Backup{}, err
	}
	if err := tw.Close(); err != nil {
		return model.Backup{}, err
	}
	if err := gz.Close(); err != nil {
		return model.Backup{}, err
	}
	if err := out.Close(); err != nil {
		return model.Backup{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return model.Backup{}, err
	}
	info, _ := os.Stat(final)
	logf("Saved %s (%d files, %s)", final, files, humanBytes(info.Size()))
	e.Store.AddEvent(ctx, "info", "backup", s.Name, "Backup "+name+" saved")
	e.uploadRemote(ctx, s.Name, name, final, logf)
	return model.Backup{Site: s.Name, File: name, Size: info.Size(), Time: now, Local: true}, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func addBytes(tw *tar.Writer, name string, b []byte, t time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: t, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, info.Size())
	return err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Prune keeps the newest keep backups of a site. Final backups (taken when a
// site is deleted) are never pruned.
func (e *Engine) Prune(ctx context.Context, site string, keep int) {
	if st, err := e.Store.Settings(ctx); err == nil && st.S3.Enabled && st.S3.LocalKeep > 0 {
		keep = st.S3.LocalKeep
	}
	bs, err := e.Backups(ctx, site)
	if err != nil {
		return
	}
	n := 0
	for _, b := range bs {
		if !b.Local || strings.HasSuffix(b.File, "-final.tar.gz") {
			continue
		}
		n++
		if n > keep {
			if p, err := e.BackupPath(ctx, site, b.File); err == nil {
				_ = os.Remove(p)
			}
		}
	}
}

// RestoreOptions says what to bring back from a backup.
type RestoreOptions struct {
	Safety bool   // back up the current site first
	What   string // all (default), files or db
}

// Restore replaces a site's files and database with a backup.
func (e *Engine) Restore(ctx context.Context, s model.Site, file string, o RestoreOptions, logf Logf) error {
	unlock := e.lockSite(s.Name)
	defer unlock()
	switch o.What {
	case "":
		o.What = "all"
	case "all", "files", "db":
	default:
		return fmt.Errorf("unknown restore option %q", o.What)
	}
	safety := o.Safety
	path, err := e.LocalBackup(ctx, s.Name, file, logf)
	if err != nil {
		return err
	}
	if safety {
		logf("Backing up the current site first")
		if _, err := e.backup(ctx, s, "", logf); err != nil {
			return fmt.Errorf("safety backup failed, so nothing was restored: %w", err)
		}
	}
	siteDir := e.Paths().SiteDir(s.Name)
	stage, err := os.MkdirTemp(e.Cfg.SitesDir, ".restore-"+s.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	logf("Unpacking %s", file)
	hasDB, err := extract(path, stage)
	if err != nil {
		return err
	}
	if o.What == "files" {
		hasDB = false
	} else if o.What == "db" && (!hasDB || s.DB == nil) {
		return errors.New("this backup has no database to restore")
	}
	if hasDB && s.DB != nil {
		logf("Restoring database %s", s.DB.Name)
		if _, err := e.sql(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`;\nCREATE DATABASE `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", s.DB.Name)); err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(stage, "database.sql"))
		if err != nil {
			return err
		}
		_, err = e.Run.Exec(ctx, sys.Cmd{Name: e.Cfg.MySQL.Client, Args: []string{"--default-character-set=utf8mb4", s.DB.Name}, Stdin: f})
		f.Close()
		if err != nil {
			return fmt.Errorf("database import: %w", err)
		}
		if err := e.ensureDB(ctx, *s.DB); err != nil {
			return err
		}
	} else if hasDB {
		logf("The backup has a database but this site has none; skipping it")
	}
	if o.What == "db" {
		e.Store.AddEvent(ctx, "warn", "backup", s.Name, "Restored the database from "+file)
		return nil
	}
	logf("Replacing files")
	ents, _ := os.ReadDir(filepath.Join(stage, "files"))
	for _, d := range ents {
		if d.Name() == "tmp" || d.Name() == "sessions" {
			continue
		}
		dst := filepath.Join(siteDir, d.Name())
		old := dst + ".berth-old"
		_ = os.RemoveAll(old)
		if _, err := os.Lstat(dst); err == nil {
			if err := os.Rename(dst, old); err != nil {
				return err
			}
		}
		if err := os.Rename(filepath.Join(stage, "files", d.Name()), dst); err != nil {
			_ = os.Rename(old, dst)
			return err
		}
		_ = os.RemoveAll(old)
	}
	logf("Fixing ownership")
	if err := e.FixPermissions(ctx, s); err != nil {
		return err
	}
	if s.Type == model.TypeWordPress && s.DB != nil {
		_ = e.WPSetDBPassword(ctx, s)
	}
	what := "Restored "
	if o.What == "files" {
		what = "Restored the files from "
	}
	e.Store.AddEvent(ctx, "warn", "backup", s.Name, what+file)
	return nil
}

// extract unpacks a backup into dir, refusing paths that escape it.
func extract(archive, dir string) (hasDB bool, err error) {
	f, err := os.Open(archive)
	if err != nil {
		return false, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return hasDB, err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return hasDB, fmt.Errorf("unsafe path %q in backup", h.Name)
		}
		if name == "database.sql" {
			hasDB = true
		}
		if name != "database.sql" && name != "site.json" && !strings.HasPrefix(name, "files"+string(filepath.Separator)) {
			continue
		}
		dst := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return hasDB, err
			}
			_ = os.Chmod(dst, os.FileMode(h.Mode).Perm())
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return hasDB, err
			}
			w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode).Perm())
			if err != nil {
				return hasDB, err
			}
			_, err = io.Copy(w, tr)
			w.Close()
			if err != nil {
				return hasDB, err
			}
			_ = os.Chtimes(dst, h.ModTime, h.ModTime)
		case tar.TypeSymlink:
			// Links may only point inside the site.
			target := h.Linkname
			resolved := filepath.Join(filepath.Dir(dst), target)
			if filepath.IsAbs(target) || !strings.HasPrefix(resolved, filepath.Join(dir, "files")+string(filepath.Separator)) {
				continue
			}
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(target, dst)
		}
	}
	return hasDB, nil
}

// DeleteBackup removes a backup here and off-site.
func (e *Engine) DeleteBackup(ctx context.Context, site, file string) error {
	if !model.ValidName(site) || !ValidBackupName(file) || !strings.HasPrefix(file, site+"-") {
		return errors.New("invalid backup name")
	}
	local := false
	if p, err := e.BackupPath(ctx, site, file); err == nil {
		if err := os.Remove(p); err != nil {
			return err
		}
		local = true
	}
	if err := e.deleteRemote(ctx, site, file); err != nil {
		return err
	}
	if !local {
		if _, _, err := e.s3(ctx); err != nil {
			return store.ErrNotFound
		}
	}
	e.index.drop(file)
	return nil
}
