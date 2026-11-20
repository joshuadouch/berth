package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/joshuadouch/berth/internal/model"
)

func TestFileManager(t *testing.T) {
	e := testEngine(t)
	s := model.Site{Name: "demo"}
	dir := filepath.Join(e.Cfg.SitesDir, "demo")
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0o755)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o600)
	_ = os.Symlink(filepath.Dir(outside), filepath.Join(dir, "public", "escape"))

	if err := e.WriteTextFile(s, "public/index.php", "<?php echo 1;"); err != nil {
		t.Fatal(err)
	}
	if txt, err := e.ReadTextFile(s, "/public/../public/index.php"); err != nil || txt != "<?php echo 1;" {
		t.Fatalf("read %q %v", txt, err)
	}
	// Symlinks out of the site are refused.
	if _, err := e.ReadTextFile(s, "public/escape/secret.txt"); err == nil {
		t.Fatal("read through a symlink out of the site")
	}
	if err := e.WriteTextFile(s, "public/escape/x.txt", "x"); err == nil {
		t.Fatal("wrote through a symlink out of the site")
	}
	if _, err := e.ReadTextFile(s, "../../etc/passwd"); err == nil {
		t.Fatal("read outside the site")
	}
	// Top-level folders are protected.
	if err := e.DeleteFiles(s, []string{"public"}); err == nil {
		t.Fatal("deleted public")
	}
	if err := e.WriteTextFile(s, "x.php", "x"); err == nil {
		t.Fatal("wrote at the top level")
	}
	if err := e.Mkdir(s, "public/assets"); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveUpload(s, "public/assets", "../../logo.png", strings.NewReader("png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "public", "assets", "logo.png")); err != nil {
		t.Fatal(err)
	}
	if err := e.RenameFile(s, "public/assets", "public/assets/inner"); err == nil {
		t.Fatal("moved a folder into itself")
	}
	if err := e.RenameFile(s, "public/assets/logo.png", "public/logo.png"); err != nil {
		t.Fatal(err)
	}
	if err := e.ChmodFile(s, "public/logo.png", 0o4755); err == nil {
		t.Fatal("allowed setuid")
	}
	// Zip extraction keeps paths inside and skips links.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("plugin/plugin.php")
	_, _ = w.Write([]byte("<?php"))
	_ = zw.Close()
	_ = os.WriteFile(filepath.Join(dir, "public", "p.zip"), buf.Bytes(), 0o644)
	if n, err := e.ExtractZip(s, "public/p.zip"); err != nil || n != 1 {
		t.Fatalf("unzip %d %v", n, err)
	}
	ents, err := e.ListFiles(s, "public")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range ents {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "assets,plugin,escape,index.php,logo.png,p.zip" {
		t.Fatalf("listing %v", names)
	}
	if err := e.DeleteFiles(s, []string{"public/plugin", "public/p.zip"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("outside file touched")
	}
}

func TestBrowseAndRestoreFiles(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	s := model.Site{Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3", Enabled: true}
	dir := filepath.Join(e.Cfg.SitesDir, "demo")
	for f, body := range map[string]string{"public/index.php": "v1", "public/wp/a.txt": "a1", "logs/php.log": "log"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644)
	}
	b, err := e.Backup(ctx, s, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	l, err := e.BrowseBackup(ctx, "demo", b.File, "public")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 2 || l.Entries[0].Name != "wp" || !l.Entries[0].Dir || l.Files != 3 {
		t.Fatalf("listing %+v", l)
	}
	var out bytes.Buffer
	if err := e.BackupFile(ctx, "demo", b.File, "public/wp/a.txt", &out); err != nil || out.String() != "a1" {
		t.Fatalf("file %q %v", out.String(), err)
	}
	_ = os.WriteFile(filepath.Join(dir, "public/index.php"), []byte("v2"), 0o644)
	_ = os.RemoveAll(filepath.Join(dir, "public/wp"))
	if err := e.RestoreFiles(ctx, s, b.File, []string{"public/wp"}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "public/wp/a.txt")); string(got) != "a1" {
		t.Fatalf("restored %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "public/index.php")); string(got) != "v2" {
		t.Fatal("restored more than asked")
	}
}

func TestS3Backups(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	backend := s3mem.New()
	_ = backend.CreateBucket("sites")
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	defer srv.Close()
	e.S3Insecure = true

	c := model.S3Settings{Enabled: true, Endpoint: strings.TrimPrefix(srv.URL, "http://"), Region: "us-east-1",
		Bucket: "sites", Prefix: "berth", AccessKey: "k", SecretKey: "s", PathStyle: true, Keep: 1}
	if err := e.TestS3(ctx, c); err != nil {
		t.Fatal(err)
	}
	st, _ := e.Store.Settings(ctx)
	st.S3 = c
	if err := st.Normalize(); err != nil {
		t.Fatal(err)
	}
	_ = e.Store.SetSettings(ctx, st)

	s := model.Site{Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3", Enabled: true}
	dir := filepath.Join(e.Cfg.SitesDir, "demo", "public")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "index.php"), []byte("one"), 0o644)
	b, err := e.Backup(ctx, s, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := e.Backups(ctx, "demo")
	if len(list) != 1 || !list[0].Local || !list[0].Remote {
		t.Fatalf("list %+v", list)
	}
	// Only off-site: restore downloads it first.
	local, _ := e.BackupPath(ctx, "demo", b.File)
	_ = os.Remove(local)
	e.index.drop(b.File)
	list, _ = e.Backups(ctx, "demo")
	if len(list) != 1 || list[0].Local || !list[0].Remote {
		t.Fatalf("list after local delete %+v", list)
	}
	_ = os.WriteFile(filepath.Join(dir, "index.php"), []byte("two"), 0o644)
	if err := e.Restore(ctx, s, b.File, RestoreOptions{What: "files"}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "index.php")); string(got) != "one" {
		t.Fatalf("restored %q", got)
	}
	// Remote retention keeps one.
	if _, err := e.Backup(ctx, model.Site{Name: "demo", Type: model.TypePHP}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	e.pruneRemote(ctx, "demo", func(string, ...any) {})
	rs, _ := e.remoteBackups(ctx)
	if len(rs) != 1 {
		t.Fatalf("remote after prune %+v", rs)
	}
	if err := e.DeleteBackup(ctx, "demo", rs[0].File); err != nil {
		t.Fatal(err)
	}
	e.forgetRemote()
	if rs, _ := e.remoteBackups(ctx); len(rs) != 0 {
		t.Fatalf("remote after delete %+v", rs)
	}
}

func TestParseUpgradable(t *testing.T) {
	out := `Listing...
php8.3-fpm/bookworm 8.3.14-1+0~20241122.66+debian12~1.gbp1 amd64 [upgradable from: 8.3.13-1+0~20241025.65+debian12~1.gbp1]
php-common/bookworm 2:95+0~20241115.53+debian12~1.gbp9d26f1 all [upgradable from: 2:94+0~20240827.52+debian12~1.gbp84b1fb]
nginx/bookworm 1.22.1-9+deb12u2 amd64 [upgradable from: 1.22.1-9+deb12u1]
`
	ups := parseUpgradable(out)
	if len(ups) != 2 || ups[0].Package != "php-common" || ups[1].PHP != "8.3" || !strings.HasPrefix(ups[1].To, "8.3.14") {
		t.Fatalf("%+v", ups)
	}
}
