package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
)

func TestWPLoginPlugin(t *testing.T) {
	e := testEngine(t)
	e.Cfg.DryRun = false
	s := model.Site{Name: "blog", Type: model.TypeWordPress}
	dir := e.Paths().SiteDir(s.Name)
	if err := os.MkdirAll(filepath.Join(dir, "public", "wp-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.installWPLoginPlugin(s); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "public", "wp-content", "mu-plugins", wpLoginFile)
	b, err := os.ReadFile(p)
	if err != nil || string(b) != wpLoginPlugin {
		t.Fatalf("plugin not written: %v", err)
	}
	// Writing again is a no-op.
	if err := e.installWPLoginPlugin(s); err != nil {
		t.Fatal(err)
	}
	if php, err := exec.LookPath("php"); err == nil {
		if out, err := exec.Command(php, "-l", p).CombinedOutput(); err != nil {
			t.Fatalf("php -l: %v\n%s", err, out)
		}
	}

	// A mu-plugins symlink pointing outside the site must not be followed.
	s2 := model.Site{Name: "evil", Type: model.TypeWordPress}
	dir2 := e.Paths().SiteDir(s2.Name)
	outside := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir2, "public", "wp-content"), 0o755)
	if err := os.Symlink(outside, filepath.Join(dir2, "public", "wp-content", "mu-plugins")); err != nil {
		t.Fatal(err)
	}
	_ = e.installWPLoginPlugin(s2)
	if _, err := os.Stat(filepath.Join(outside, wpLoginFile)); err == nil {
		t.Fatal("wrote through a symlink outside the site")
	}
}

func TestCleanWPOutput(t *testing.T) {
	in := "PHP Warning:  The magic method Vc_Manager::__wakeup() must have public visibility in /srv/x/class-vc-manager.php on line 203\n" +
		"Warning: The magic method Vc_Manager::__wakeup() must have public visibility in /srv/x/class-vc-manager.php on line 203\n" +
		"https://example.com"
	if got := strings.TrimSpace(cleanWPOutput(in)); got != "https://example.com" {
		t.Fatalf("got %q", got)
	}
	if got := cleanWPOutput("[{\"name\":\"a\"}]"); got != "[{\"name\":\"a\"}]" {
		t.Fatalf("clean output changed: %q", got)
	}
}

func TestNewestVersion(t *testing.T) {
	out := "PHP Warning:  The magic method X::__wakeup() must have public visibility in /x.php on line 3\nversion\n6.8.3\n6.7.4\n5.9.12\n"
	if got := newestVersion(out, "5.9.11"); got != "6.8.3" {
		t.Fatalf("got %q", got)
	}
	if got := newestVersion("Success: WordPress is at the latest version.", "6.8.3"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := newestVersion("version\n6.8.3\n", "6.8.3"); got != "" {
		t.Fatalf("same version is not an update: %q", got)
	}
}

func TestPerformanceNeverNull(t *testing.T) {
	e := testEngine(t)
	s := model.Site{Name: "blog", Type: model.TypeWordPress, DB: &model.Database{User: "site_blog"}}
	dir := e.Paths().SiteDir(s.Name)
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "logs", "php-slow.log"), nil, 0o644) // FPM creates it empty
	p := e.SitePerformance(context.Background(), s)
	if p.PHPSlow == nil || p.Culprits == nil || p.DBSlow == nil {
		t.Fatalf("lists must not be nil: %+v", p)
	}
}
