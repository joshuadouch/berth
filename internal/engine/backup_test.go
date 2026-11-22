package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

func testEngine(t *testing.T) *Engine {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir, cfg.SitesDir, cfg.LogDir = filepath.Join(dir, "data"), filepath.Join(dir, "sites"), filepath.Join(dir, "log")
	cfg.Nginx.ConfDir = filepath.Join(dir, "nginx")
	cfg.PHP.PoolDir = filepath.Join(dir, "php", "{v}")
	cfg.DryRun = true
	cfg.ACMEDir = filepath.Join(dir, "acme")
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	st, err := store.Open(filepath.Join(cfg.DataDir, "berth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings := model.Settings{BackupDir: filepath.Join(dir, "backups")}
	_ = settings.Normalize()
	if err := st.SetSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	return New(cfg, st, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

func TestBackupRoundTrip(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	s := model.Site{Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3", Enabled: true}
	site := filepath.Join(e.Cfg.SitesDir, "demo")
	for _, f := range []string{"public/index.php", "public/sub/a.txt", "tmp/skip.txt", "sessions/sess_x"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(site, f)), 0o755)
		if err := os.WriteFile(filepath.Join(site, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink("sub/a.txt", filepath.Join(site, "public", "link"))
	b, err := e.Backup(ctx, s, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidBackupName(b.File) {
		t.Fatalf("bad name %s", b.File)
	}
	list, _ := e.Backups(ctx, "")
	if len(list) != 1 || list[0].File != b.File {
		t.Fatalf("list %+v", list)
	}
	p, err := e.BackupPath(ctx, "demo", b.File)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := extract(p, out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "files", "public", "sub", "a.txt")); string(got) != "public/sub/a.txt" {
		t.Fatalf("content %q", got)
	}
	if l, _ := os.Readlink(filepath.Join(out, "files", "public", "link")); l != "sub/a.txt" {
		t.Fatalf("link %q", l)
	}
	for _, skipped := range []string{"files/tmp/skip.txt", "files/sessions/sess_x"} {
		if _, err := os.Stat(filepath.Join(out, skipped)); err == nil {
			t.Errorf("%s should not be backed up", skipped)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "site.json")); err != nil {
		t.Error("site.json missing")
	}
	for _, bad := range []string{"../demo-20260101-000000.tar.gz", "other-20260101-000000.tar.gz", "demo.tar.gz"} {
		if _, err := e.BackupPath(ctx, "demo", bad); err == nil {
			t.Errorf("BackupPath accepted %q", bad)
		}
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../evil", "/etc/evil", "files/../../evil"} {
		p := filepath.Join(dir, "bad.tar.gz")
		f, _ := os.Create(p)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("x"))
		tw.Close()
		gz.Close()
		f.Close()
		if _, err := extract(p, filepath.Join(dir, "out")); err == nil {
			t.Errorf("extract accepted %q", name)
		}
	}
	// A symlink pointing outside the site is dropped.
	p := filepath.Join(dir, "link.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "files/public/passwd", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	_ = tw.WriteHeader(&tar.Header{Name: "files/public/up", Linkname: "../../../../etc", Typeflag: tar.TypeSymlink})
	tw.Close()
	gz.Close()
	f.Close()
	out := filepath.Join(dir, "out2")
	if _, err := extract(p, out); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"passwd", "up"} {
		if _, err := os.Lstat(filepath.Join(out, "files", "public", n)); err == nil {
			t.Errorf("unsafe link %s was created", n)
		}
	}
}

func TestApplyDryRun(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	s := model.Site{Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3", Enabled: true, Status: model.StatusActive}
	_ = s.Normalize()
	if err := e.Store.SaveSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	vhost := filepath.Join(e.Cfg.Nginx.ConfDir, "sites", "demo.conf")
	pool := filepath.Join(e.Cfg.SitesDir, "..", "php", "8.3", "berth-demo.conf")
	for _, f := range []string{vhost, pool} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s not written: %v", f, err)
		}
	}
	// Removing the site removes its files.
	if err := e.Store.DeleteSite(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{vhost, pool} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("%s should be removed", f)
		}
	}
}
