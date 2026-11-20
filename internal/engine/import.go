package engine

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/sys"
)

// Migrating a site in: the admin uploads a files archive (.zip, .tar,
// .tar.gz) and/or a database dump (.sql, .sql.gz) before creating the site.
// Uploads wait in <data>/imports until the site is created from them.

var importIDRe = regexp.MustCompile(`^[0-9a-f]{24}\.(zip|tar|tar\.gz|tgz|sql|sql\.gz)$`)

func (e *Engine) importDir() string { return filepath.Join(e.Cfg.DataDir, "imports") }

func importKind(name string) (string, error) {
	n := strings.ToLower(name)
	for _, ext := range []string{".tar.gz", ".tgz", ".sql.gz", ".zip", ".tar", ".sql"} {
		if strings.HasSuffix(n, ext) {
			return ext[1:], nil
		}
	}
	return "", errors.New("upload a .zip, .tar, .tar.gz, .sql or .sql.gz file")
}

// UploadInfo describes a staged upload.
type UploadInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // files or db
}

// SaveImport stores an uploaded archive or dump for a new site.
func (e *Engine) SaveImport(name string, r io.Reader) (UploadInfo, error) {
	ext, err := importKind(name)
	if err != nil {
		return UploadInfo{}, err
	}
	if err := os.MkdirAll(e.importDir(), 0o700); err != nil {
		return UploadInfo{}, err
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b) + "." + ext
	p := filepath.Join(e.importDir(), id)
	f, err := os.OpenFile(p+".partial", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return UploadInfo{}, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(p+".partial", p)
	}
	if err != nil {
		os.Remove(p + ".partial")
		return UploadInfo{}, err
	}
	kind := "files"
	if strings.HasPrefix(ext, "sql") {
		kind = "db"
	}
	return UploadInfo{ID: id, Name: filepath.Base(name), Size: n, Kind: kind}, nil
}

func (e *Engine) importPath(id string) (string, error) {
	if !importIDRe.MatchString(id) {
		return "", errors.New("invalid upload")
	}
	p := filepath.Join(e.importDir(), id)
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("the upload has expired or was removed; upload it again")
	}
	return p, nil
}

// DeleteImport removes a staged upload.
func (e *Engine) DeleteImport(id string) error {
	p, err := e.importPath(id)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// cleanImports drops uploads nobody used within two days.
func (e *Engine) cleanImports(ctx context.Context) {
	sites, _ := e.Store.Sites(ctx)
	inUse := map[string]bool{}
	for _, s := range sites {
		if s.Import != nil {
			inUse[s.Import.Files], inUse[s.Import.DB] = true, true
		}
	}
	ents, _ := os.ReadDir(e.importDir())
	for _, d := range ents {
		info, err := d.Info()
		if err != nil || inUse[d.Name()] || time.Since(info.ModTime()) < 48*time.Hour {
			continue
		}
		_ = os.Remove(filepath.Join(e.importDir(), d.Name()))
	}
}

// checkImport validates a new site's import against its type.
func (e *Engine) checkImport(s *model.Site) error {
	im := s.Import
	if im == nil || (im.Files == "" && im.DB == "") {
		s.Import = nil
		return nil
	}
	if im.Files != "" {
		if _, err := e.importPath(im.Files); err != nil {
			return err
		}
		if strings.Contains(im.Files, ".sql") {
			return errors.New("the files upload is a database dump")
		}
	}
	if im.DB != "" {
		if _, err := e.importPath(im.DB); err != nil {
			return err
		}
		if !strings.Contains(im.DB, ".sql") {
			return errors.New("the database upload should be a .sql or .sql.gz dump")
		}
		if !s.Type.HasDB() {
			return errors.New("a PHP site has no database; choose PHP + database to import a dump")
		}
	}
	if s.Type == model.TypeWordPress && (im.Files == "" || im.DB == "") {
		return errors.New("to move a WordPress site in, upload both its files and its database dump")
	}
	return nil
}

// runImport puts the uploaded files and database in place. It replaces the
// WordPress install for imported WordPress sites.
func (e *Engine) runImport(ctx context.Context, s model.Site, logf Logf) error {
	im := s.Import
	if e.Cfg.DryRun {
		return nil
	}
	public := filepath.Join(e.Paths().SiteDir(s.Name), "public")
	var strayDump string
	if im.Files != "" {
		p, err := e.importPath(im.Files)
		if err != nil {
			return err
		}
		stage, err := os.MkdirTemp(e.Cfg.SitesDir, ".import-"+s.Name+"-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		logf("Unpacking %s", im.FilesName)
		n, err := extractArchive(p, stage)
		if err != nil {
			return fmt.Errorf("unpacking %s: %w", im.FilesName, err)
		}
		logf("Unpacked %d files", n)
		root, err := findWebRoot(stage, s.Type == model.TypeWordPress)
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(stage, root); rel != "." {
			logf("Using %s/ from the archive as the web root", rel)
			if im.DB == "" && s.DB != nil {
				strayDump = findDump(stage, root)
			}
		}
		// The new site's document root may be a subfolder (Laravel); the
		// archive is the whole app, so it goes into public.
		ents, _ := os.ReadDir(public)
		for _, d := range ents {
			_ = os.RemoveAll(filepath.Join(public, d.Name()))
		}
		ents, err = os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, d := range ents {
			if err := os.Rename(filepath.Join(root, d.Name()), filepath.Join(public, d.Name())); err != nil {
				return err
			}
		}
		if err := e.FixPermissions(ctx, s); err != nil {
			return err
		}
	}
	dump := ""
	if im.DB != "" {
		p, err := e.importPath(im.DB)
		if err != nil {
			return err
		}
		dump = p
	} else if strayDump != "" {
		logf("Found %s next to the site files; importing it", filepath.Base(strayDump))
		dump = strayDump
	}
	if dump != "" && s.DB != nil {
		logf("Importing the database into %s", s.DB.Name)
		if err := e.importDump(ctx, *s.DB, dump); err != nil {
			return fmt.Errorf("database import: %w", err)
		}
	}
	if s.Type == model.TypeWordPress {
		return e.adoptWordPress(ctx, s, logf)
	}
	return nil
}

// finishImport deletes a site's uploads once it was built from them.
func (e *Engine) finishImport(ctx context.Context, name string) {
	cur, err := e.Store.Site(ctx, name)
	if err != nil || cur.Import == nil {
		return
	}
	for _, id := range []string{cur.Import.Files, cur.Import.DB} {
		if p, err := e.importPath(id); err == nil {
			_ = os.Remove(p)
		}
	}
	cur.Import = nil
	_ = e.Store.SaveSite(ctx, cur)
}

// extractArchive unpacks a zip or (gzipped) tar into dir. Links, devices and
// paths that leave dir are skipped.
func extractArchive(archive, dir string) (int, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	f, err := os.Open(archive)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	head := make([]byte, 4)
	_, _ = io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	n := 0
	write := func(name string, mode os.FileMode, r io.Reader) error {
		if hasDotDot(name) {
			return nil // never written outside where the archive says
		}
		rel, err := cleanRel(name)
		if err != nil || rel == "." {
			return nil
		}
		if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode&0o111 != 0 {
			perm = 0o755
		}
		_ = root.RemoveAll(rel)
		w, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, r)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			n++
		}
		return err
	}
	if bytes.HasPrefix(head, []byte("PK")) {
		st, err := f.Stat()
		if err != nil {
			return 0, err
		}
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return 0, err
		}
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				if rel, err := cleanRel(zf.Name); err == nil && rel != "." {
					_ = root.MkdirAll(rel, 0o755)
				}
				continue
			}
			if !zf.Mode().IsRegular() {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return n, err
			}
			err = write(zf.Name, zf.Mode(), rc)
			rc.Close()
			if err != nil {
				return n, err
			}
		}
		return n, nil
	}
	var r io.Reader = bufio.NewReaderSize(f, 1<<20)
	if bytes.HasPrefix(head, []byte{0x1f, 0x8b}) {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, fmt.Errorf("not a zip or tar archive (%v)", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if rel, err := cleanRel(h.Name); err == nil && rel != "." {
				_ = root.MkdirAll(rel, 0o755)
			}
		case tar.TypeReg:
			if err := write(h.Name, os.FileMode(h.Mode), tr); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func hasDotDot(name string) bool {
	for _, p := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

var webRootNames = []string{"public_html", "htdocs", "httpdocs", "www", "html", "web"}

// findWebRoot picks the folder of the archive that holds the site: the one
// with wp-load.php for WordPress, otherwise the archive itself unless it is a
// single wrapping folder or a hosting account with a public_html.
func findWebRoot(stage string, wordpress bool) (string, error) {
	if wordpress {
		best, depth := "", 99
		_ = filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(stage, p)
			dep := strings.Count(rel, string(filepath.Separator))
			if d.IsDir() && dep > 4 {
				return filepath.SkipDir
			}
			if !d.IsDir() && d.Name() == "wp-load.php" && dep < depth {
				best, depth = filepath.Dir(p), dep
			}
			return nil
		})
		if best == "" {
			return "", errors.New("the archive does not contain a WordPress site (no wp-load.php found)")
		}
		return best, nil
	}
	dir := stage
	for i := 0; i < 3; i++ {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		var dirs []string
		files := 0
		for _, d := range ents {
			if d.IsDir() {
				if d.Name() != "__MACOSX" {
					dirs = append(dirs, d.Name())
				}
			} else if !strings.HasPrefix(d.Name(), ".") && !strings.HasSuffix(strings.ToLower(d.Name()), ".sql") {
				files++
			}
		}
		if files == 0 && len(dirs) == 1 {
			dir = filepath.Join(dir, dirs[0])
			continue
		}
		if !hasIndex(dir) {
			for _, n := range webRootNames {
				if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.IsDir() && hasIndex(filepath.Join(dir, n)) {
					return filepath.Join(dir, n), nil
				}
			}
		}
		break
	}
	return dir, nil
}

func hasIndex(dir string) bool {
	for _, n := range []string{"index.php", "index.html", "index.htm", "artisan", "composer.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// findDump returns the one .sql file outside the web root, if there is
// exactly one (hosting backups often keep the dump beside public_html).
func findDump(stage, root string) string {
	var found []string
	_ = filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (p == root || strings.Count(p[len(stage):], string(filepath.Separator)) > 3) {
			return filepath.SkipDir
		}
		n := strings.ToLower(d.Name())
		if !d.IsDir() && (strings.HasSuffix(n, ".sql") || strings.HasSuffix(n, ".sql.gz")) {
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// importDump loads a dump as the site's own database user, so statements
// that name other databases are refused by MariaDB. DEFINER clauses (which
// need privileges the site lacks) are dropped and MySQL 8 collations mapped
// to MariaDB ones.
func (e *Engine) importDump(ctx context.Context, db model.Database, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	var r io.Reader = br
	if head, _ := br.Peek(2); bytes.Equal(head, []byte{0x1f, 0x8b}) {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(filterDump(r, pw)) }()
	defer pr.Close()
	_, err = e.Run.Exec(ctx, sys.Cmd{Name: e.Cfg.MySQL.Client,
		Args:  []string{"--user=" + db.User, "--default-character-set=utf8mb4", "--max-allowed-packet=1G", db.Name},
		Env:   []string{"MYSQL_PWD=" + db.Password, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"},
		Stdin: pr})
	return err
}

var (
	definerRe   = regexp.MustCompile("(?i)\\s*DEFINER\\s*=\\s*(`[^`]*`|'[^']*'|[^\\s@]+)@(`[^`]*`|'[^']*'|[^\\s*]+)")
	securityRe  = regexp.MustCompile(`(?i)/\*!50013\s+SQL SECURITY DEFINER\s*\*/`)
	collationRe = regexp.MustCompile(`utf8mb4_0900_[a-z_]+`)
	useDBRe     = regexp.MustCompile("(?i)^\\s*(USE\\s+`?[^`;\\s]+`?\\s*;|CREATE DATABASE\\b.*;)\\s*$")
)

// filterDump copies a dump line by line, making it fit a MariaDB site
// database: no DEFINER clauses, no USE or CREATE DATABASE, MariaDB
// collations.
func filterDump(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 1<<20)
	bw := bufio.NewWriterSize(w, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			// Only short statement lines carry these; skip the regexes on
			// long INSERT lines.
			if len(line) < 4096 && !bytes.HasPrefix(line, []byte("INSERT ")) && !bytes.HasPrefix(line, []byte("(")) {
				if useDBRe.Match(line) {
					line = []byte("-- (removed by Berth) " + strings.TrimSpace(string(line)) + "\n")
				} else {
					line = definerRe.ReplaceAll(line, nil)
					line = securityRe.ReplaceAll(line, nil)
				}
			}
			if bytes.Contains(line, []byte("utf8mb4_0900_")) {
				line = collationRe.ReplaceAll(line, []byte("utf8mb4_unicode_ci"))
			}
			if _, werr := bw.Write(line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return bw.Flush()
}

// adoptWordPress points an imported WordPress at its new database and
// address.
func (e *Engine) adoptWordPress(ctx context.Context, s model.Site, logf Logf) error {
	root := filepath.Join(e.Paths().SiteDir(s.Name), "public", s.DocRoot)
	prefix := "wp_"
	if out, err := e.sql(ctx, fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_NAME LIKE '%%options' ORDER BY LENGTH(TABLE_NAME)", s.DB.Name)); err == nil {
		if first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]); first != "" {
			prefix = strings.TrimSuffix(first, "options")
		}
	}
	if prefix == "" {
		return errors.New("the database dump has no WordPress options table")
	}
	if _, err := os.Stat(filepath.Join(root, "wp-config.php")); err != nil {
		logf("Writing wp-config.php (table prefix %s)", prefix)
		if _, err := e.wp(ctx, s, wpExtraPHP, "config", "create", "--skip-check",
			"--dbname="+s.DB.Name, "--dbuser="+s.DB.User, "--dbpass="+s.DB.Password, "--dbhost=localhost",
			"--dbcharset=utf8mb4", "--dbprefix="+prefix, "--extra-php"); err != nil {
			return err
		}
	} else {
		logf("Pointing wp-config.php at the new database")
		for k, v := range map[string]string{"DB_NAME": s.DB.Name, "DB_USER": s.DB.User, "DB_PASSWORD": s.DB.Password, "DB_HOST": "localhost"} {
			if _, err := e.wp(ctx, s, "", "config", "set", k, v, "--type=constant"); err != nil {
				return err
			}
		}
		_, _ = e.wp(ctx, s, "", "config", "set", "table_prefix", prefix, "--type=variable")
		_, _ = e.wp(ctx, s, "", "config", "set", "FS_METHOD", "direct", "--type=constant")
		_, _ = e.wp(ctx, s, "", "config", "set", "DISABLE_WP_CRON", "true", "--raw", "--type=constant")
		// Hard-coded addresses would override the new domain.
		for _, k := range []string{"WP_HOME", "WP_SITEURL"} {
			_, _ = e.wp(ctx, s, "", "config", "delete", k, "--type=constant")
		}
	}
	old, err := e.wp(ctx, s, "", "option", "get", "siteurl", "--skip-plugins", "--skip-themes")
	if err != nil {
		return fmt.Errorf("WordPress cannot read its database: %w", err)
	}
	home, _ := e.wp(ctx, s, "", "option", "get", "home", "--skip-plugins", "--skip-themes")
	old, home = strings.TrimRight(strings.TrimSpace(old), "/"), strings.TrimRight(strings.TrimSpace(home), "/")
	if home != "" && home != old && home != s.URL() {
		if err := e.WPReplaceURL(ctx, s, home, logf); err != nil {
			return err
		}
	}
	if old != s.URL() {
		if err := e.WPReplaceURL(ctx, s, old, logf); err != nil {
			return err
		}
		// Links written with the other scheme ("http://old" on an https
		// site) still name the old host.
		oldHost := strings.TrimPrefix(strings.TrimPrefix(old, "https:"), "http:")
		newHost := strings.TrimPrefix(strings.TrimPrefix(s.URL(), "https:"), "http:")
		if strings.HasPrefix(oldHost, "//") && oldHost != newHost {
			if _, err := e.wp(ctx, s, "", "search-replace", oldHost, newHost, "--all-tables-with-prefix", "--skip-columns=guid",
				"--precise", "--skip-plugins", "--skip-themes"); err != nil {
				logf("Could not replace %s: %v", oldHost, err)
			}
		}
	} else {
		logf("The site already uses %s", s.URL())
	}
	return nil
}
