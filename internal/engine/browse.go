package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// Browsing a backup reads the archive once into an index of its files, kept
// for the few most recently opened backups.

type indexEntry struct {
	Size    int64
	Mode    int64
	ModTime time.Time
	Dir     bool
	Link    string
}

type backupIndex struct {
	files map[string]indexEntry // "public/index.php" (paths under files/)
	hasDB bool
	dbLen int64
	used  time.Time
}

type indexCache struct {
	mu sync.Mutex
	m  map[string]*backupIndex
}

func (c *indexCache) drop(file string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, file)
}

func (e *Engine) backupIndex(ctx context.Context, site, file string) (*backupIndex, string, error) {
	p, err := e.LocalBackup(ctx, site, file, func(string, ...any) {})
	if err != nil {
		return nil, "", err
	}
	e.index.mu.Lock()
	defer e.index.mu.Unlock()
	if e.index.m == nil {
		e.index.m = map[string]*backupIndex{}
	}
	if ix, ok := e.index.m[file]; ok {
		ix.used = time.Now()
		return ix, p, nil
	}
	ix := &backupIndex{files: map[string]indexEntry{}}
	err = walkBackup(p, func(h *tar.Header, _ io.Reader) (bool, error) {
		if h.Name == "database.sql" {
			ix.hasDB, ix.dbLen = true, h.Size
			return false, nil
		}
		rel, ok := strings.CutPrefix(h.Name, "files/")
		if !ok {
			return false, nil
		}
		rel = strings.TrimSuffix(path.Clean("/"+rel), "/")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return false, nil
		}
		ix.files[rel] = indexEntry{Size: h.Size, Mode: h.Mode, ModTime: h.ModTime, Dir: h.Typeflag == tar.TypeDir, Link: h.Linkname}
		// Make sure parent folders appear even if the archive skips them.
		for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
			if _, ok := ix.files[d]; ok {
				break
			}
			ix.files[d] = indexEntry{Dir: true, Mode: 0o755}
		}
		return false, nil
	})
	if err != nil {
		return nil, "", err
	}
	ix.used = time.Now()
	if len(e.index.m) >= 4 {
		var oldest string
		for k, v := range e.index.m {
			if oldest == "" || v.used.Before(e.index.m[oldest].used) {
				oldest = k
			}
		}
		delete(e.index.m, oldest)
	}
	e.index.m[file] = ix
	return ix, p, nil
}

// walkBackup calls fn for every entry; fn returns true to stop.
func walkBackup(archive string, fn func(h *tar.Header, r io.Reader) (bool, error)) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		stop, err := fn(h, tr)
		if err != nil || stop {
			return err
		}
	}
}

// BackupListing is one folder inside a backup.
type BackupListing struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
	HasDB   bool        `json:"has_db"`
	DBSize  int64       `json:"db_size"`
	Files   int         `json:"files"`
}

// BrowseBackup lists a folder inside a backup ("" is the site folder).
func (e *Engine) BrowseBackup(ctx context.Context, site, file, dir string) (BackupListing, error) {
	ix, _, err := e.backupIndex(ctx, site, file)
	if err != nil {
		return BackupListing{}, err
	}
	rel, err := cleanRel(dir)
	if err != nil {
		return BackupListing{}, err
	}
	if rel != "." {
		if en, ok := ix.files[rel]; !ok || !en.Dir {
			return BackupListing{}, store.ErrNotFound
		}
	}
	out := BackupListing{Path: rel, Entries: []FileEntry{}, HasDB: ix.hasDB, DBSize: ix.dbLen}
	prefix := rel + "/"
	if rel == "." {
		prefix = ""
	}
	for name, en := range ix.files {
		if !en.Dir {
			out.Files++
		}
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok || rest == "" || strings.Contains(rest, "/") {
			continue
		}
		out.Entries = append(out.Entries, FileEntry{Name: rest, Dir: en.Dir, Link: en.Link, Size: en.Size,
			Mode: fmt.Sprintf("%04o", en.Mode&0o7777), ModTime: en.ModTime})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// BackupFile streams one file out of a backup to w.
func (e *Engine) BackupFile(ctx context.Context, site, file, p string, w io.Writer) error {
	ix, archive, err := e.backupIndex(ctx, site, file)
	if err != nil {
		return err
	}
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	var want string
	switch {
	case rel == "database.sql" && p == "database.sql" && ix.hasDB:
		want = "database.sql"
	default:
		en, ok := ix.files[rel]
		if !ok || en.Dir || en.Link != "" {
			return store.ErrNotFound
		}
		want = "files/" + rel
	}
	found := false
	err = walkBackup(archive, func(h *tar.Header, r io.Reader) (bool, error) {
		if strings.TrimSuffix(h.Name, "/") != want || h.Typeflag != tar.TypeReg {
			return false, nil
		}
		found = true
		_, err := io.Copy(w, r)
		return true, err
	})
	if err == nil && !found {
		return store.ErrNotFound
	}
	return err
}

// RestoreFiles puts chosen files and folders from a backup back into the
// site, overwriting what is there. Nothing else changes.
func (e *Engine) RestoreFiles(ctx context.Context, s model.Site, file string, paths []string, logf Logf) error {
	unlock := e.waitSite(s.Name, logf)
	defer unlock()
	ix, archive, err := e.backupIndex(ctx, s.Name, file)
	if err != nil {
		return err
	}
	var want []string
	for _, p := range paths {
		rel, err := cleanRel(p)
		if err != nil {
			return err
		}
		if err := writable(rel); err != nil {
			return err
		}
		if _, ok := ix.files[rel]; !ok {
			return fmt.Errorf("%s is not in this backup", rel)
		}
		want = append(want, rel)
	}
	if len(want) == 0 {
		return errors.New("choose what to restore")
	}
	match := func(rel string) bool {
		for _, w := range want {
			if rel == w || strings.HasPrefix(rel, w+"/") {
				return true
			}
		}
		return false
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	n := 0
	err = walkBackup(archive, func(h *tar.Header, tr io.Reader) (bool, error) {
		rel, ok := strings.CutPrefix(h.Name, "files/")
		if !ok {
			return false, nil
		}
		rel, err := cleanRel(rel)
		if err != nil || rel == "." || !match(rel) {
			return false, nil
		}
		if err := ctx.Err(); err != nil {
			return true, err
		}
		mode := os.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := mkdirAllOwned(e, r, s, rel); err != nil {
				return true, err
			}
			_ = r.Chmod(rel, mode)
		case tar.TypeReg:
			if err := mkdirAllOwned(e, r, s, path.Dir(rel)); err != nil {
				return true, err
			}
			if st, err := r.Lstat(rel); err == nil && (st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
				if err := r.RemoveAll(rel); err != nil {
					return true, err
				}
			}
			tmp := path.Join(path.Dir(rel), ".berth-restore-"+path.Base(rel))
			f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return true, err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = r.Remove(tmp)
				return true, err
			}
			_ = r.Chmod(tmp, mode)
			e.chownNew(r, s, tmp)
			if err := r.Rename(tmp, rel); err != nil {
				return true, err
			}
			n++
			logf("Restored %s", rel)
		case tar.TypeSymlink:
			target := h.Linkname
			resolved := path.Join(path.Dir(rel), target)
			if path.IsAbs(target) || strings.HasPrefix(resolved, "../") || resolved == ".." {
				return false, nil
			}
			_ = mkdirAllOwned(e, r, s, path.Dir(rel))
			_ = r.RemoveAll(rel)
			if err := r.Symlink(target, rel); err == nil {
				e.chownNew(r, s, rel)
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	logf("Restored %d files from %s", n, filepath.Base(archive))
	e.Store.AddEvent(ctx, "warn", "backup", s.Name, fmt.Sprintf("Restored %s from %s", strings.Join(want, ", "), file))
	return nil
}
