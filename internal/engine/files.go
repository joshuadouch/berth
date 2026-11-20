package engine

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joshuadouch/berth/internal/model"
)

// The file manager works inside a site's directory through os.Root, so a
// symlink a site plants can never lead Berth (running as root) outside it.
// New files belong to the site user.

// FileEntry is one item in a folder listing.
type FileEntry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Link    string    `json:"link,omitempty"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mtime"`
}

// cleanRel turns a user path into a clean path relative to the site root.
func cleanRel(p string) (string, error) {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		p = "."
	}
	if strings.Contains(p, "\x00") {
		return "", errors.New("invalid path")
	}
	return p, nil
}

// writable reports whether rel may be created, changed or removed.
func writable(rel string) error {
	if rel == "." || !strings.Contains(rel, "/") {
		return errors.New("the site's top-level folders cannot be changed; work inside public")
	}
	return nil
}

func (e *Engine) siteRoot(s model.Site) (*os.Root, error) {
	return os.OpenRoot(e.Paths().SiteDir(s.Name))
}

func (e *Engine) owner(s model.Site) (int, int) {
	uid, gid, _, err := e.ids(s.User())
	if err != nil || e.Cfg.DryRun {
		return -1, -1
	}
	return uid, gid
}

func (e *Engine) chownNew(r *os.Root, s model.Site, rel string) {
	if uid, gid := e.owner(s); uid >= 0 {
		_ = r.Lchown(rel, uid, gid)
	}
}

// ListFiles lists a folder in the site.
func (e *Engine) ListFiles(s model.Site, p string) ([]FileEntry, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return nil, err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := []FileEntry{}
	for _, de := range ents {
		info, err := de.Info()
		if err != nil {
			continue
		}
		fe := FileEntry{Name: de.Name(), Dir: info.IsDir(), Size: info.Size(), Mode: fmt.Sprintf("%04o", info.Mode().Perm()), ModTime: info.ModTime()}
		if info.Mode()&os.ModeSymlink != 0 {
			fe.Link, _ = r.Readlink(path.Join(rel, de.Name()))
			if st, err := r.Stat(path.Join(rel, de.Name())); err == nil && st.IsDir() {
				fe.Dir = true
			}
		}
		if fe.Dir {
			fe.Size = 0
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// MaxEditBytes is the largest file the editor opens.
const MaxEditBytes = 2 << 20

// ReadTextFile returns a file's text for the editor.
func (e *Engine) ReadTextFile(s model.Site, p string) (string, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return "", err
	}
	defer r.Close()
	st, err := r.Stat(rel)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", errors.New("that is a folder")
	}
	if st.Size() > MaxEditBytes {
		return "", errors.New("the file is too large to edit here (over 2 MB); download it instead")
	}
	b, err := r.ReadFile(rel)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", errors.New("this looks like a binary file; download it instead")
	}
	return string(b), nil
}

// WriteTextFile saves a file from the editor, keeping its mode.
func (e *Engine) WriteTextFile(s model.Site, p, content string) error {
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	if err := writable(rel); err != nil {
		return err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	mode := os.FileMode(0o644)
	isNew := true
	if st, err := r.Stat(rel); err == nil {
		if st.IsDir() {
			return errors.New("that is a folder")
		}
		mode, isNew = st.Mode().Perm(), false
	}
	f, err := r.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if isNew {
		e.chownNew(r, s, rel)
	}
	return nil
}

// OpenFile opens a file for download.
func (e *Engine) OpenFile(s model.Site, p string) (*os.File, fs.FileInfo, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return nil, nil, err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, nil, errors.New("not a file")
	}
	return f, st, nil
}

// SaveUpload writes an uploaded file into dir.
func (e *Engine) SaveUpload(s model.Site, dir, name string, src io.Reader) error {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == ".." || name == "" {
		return errors.New("invalid file name")
	}
	rel, err := cleanRel(path.Join(dir, name))
	if err != nil {
		return err
	}
	if err := writable(rel); err != nil {
		return err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := path.Join(path.Dir(rel), ".berth-upload-"+name)
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = r.Remove(tmp)
		return err
	}
	e.chownNew(r, s, tmp)
	if st, err := r.Lstat(rel); err == nil && st.IsDir() {
		_ = r.Remove(tmp)
		return fmt.Errorf("%s is a folder", name)
	}
	return r.Rename(tmp, rel)
}

// Mkdir creates a folder.
func (e *Engine) Mkdir(s model.Site, p string) error {
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	if err := writable(rel); err != nil {
		return err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Mkdir(rel, 0o755); err != nil {
		return err
	}
	e.chownNew(r, s, rel)
	return nil
}

// MkdirAll creates a folder and its parents (folder uploads).
func (e *Engine) MkdirAll(s model.Site, p string) error {
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	if err := writable(rel); err != nil {
		return err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	return mkdirAllOwned(e, r, s, rel)
}

// RenameFile moves or renames a file or folder within the site.
func (e *Engine) RenameFile(s model.Site, from, to string) error {
	a, err := cleanRel(from)
	if err != nil {
		return err
	}
	b, err := cleanRel(to)
	if err != nil {
		return err
	}
	if err := writable(a); err != nil {
		return err
	}
	if err := writable(b); err != nil {
		return err
	}
	if b == a || strings.HasPrefix(b, a+"/") {
		return errors.New("cannot move a folder into itself")
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(b); err == nil {
		return fmt.Errorf("%s already exists", b)
	}
	return r.Rename(a, b)
}

// DeleteFiles removes files and folders.
func (e *Engine) DeleteFiles(s model.Site, paths []string) error {
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, p := range paths {
		rel, err := cleanRel(p)
		if err != nil {
			return err
		}
		if err := writable(rel); err != nil {
			return err
		}
		if err := r.RemoveAll(rel); err != nil {
			return err
		}
	}
	return nil
}

// ChmodFile changes a file's permissions.
func (e *Engine) ChmodFile(s model.Site, p string, mode os.FileMode) error {
	rel, err := cleanRel(p)
	if err != nil {
		return err
	}
	if err := writable(rel); err != nil {
		return err
	}
	if mode&0o7000 != 0 {
		return errors.New("setuid, setgid and sticky bits are not allowed")
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.Chmod(rel, mode.Perm())
}

// ExtractZip unpacks a .zip file into the folder that holds it.
func (e *Engine) ExtractZip(s model.Site, p string) (int, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return 0, err
	}
	if err := writable(rel); err != nil {
		return 0, err
	}
	r, err := e.siteRoot(s)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return 0, fmt.Errorf("not a zip file: %w", err)
	}
	base := path.Dir(rel)
	n := 0
	for _, zf := range zr.File {
		name, err := cleanRel(zf.Name)
		if err != nil || name == "." || strings.HasPrefix(zf.Name, "/") || strings.Contains(zf.Name, "../") {
			return n, fmt.Errorf("unsafe path %q in zip", zf.Name)
		}
		dst := path.Join(base, name)
		if zf.FileInfo().IsDir() {
			if err := r.MkdirAll(dst, 0o755); err != nil {
				return n, err
			}
			e.chownNew(r, s, dst)
			continue
		}
		if !zf.Mode().IsRegular() {
			continue // no symlinks or devices from archives
		}
		if err := mkdirAllOwned(e, r, s, path.Dir(dst)); err != nil {
			return n, err
		}
		src, err := zf.Open()
		if err != nil {
			return n, err
		}
		mode := zf.Mode().Perm() | 0o600
		if mode&0o111 != 0 {
			mode = 0o755
		} else {
			mode = 0o644
		}
		_ = r.Remove(dst)
		w, err := r.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			src.Close()
			return n, err
		}
		_, err = io.Copy(w, src)
		src.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, err
		}
		e.chownNew(r, s, dst)
		n++
	}
	return n, nil
}

func mkdirAllOwned(e *Engine, r *os.Root, s model.Site, dir string) error {
	if dir == "." || dir == "" {
		return nil
	}
	if st, err := r.Stat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s is not a folder", dir)
		}
		return nil
	}
	if err := mkdirAllOwned(e, r, s, path.Dir(dir)); err != nil {
		return err
	}
	if err := r.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	e.chownNew(r, s, dir)
	return nil
}
