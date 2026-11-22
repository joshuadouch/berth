package engine

import (
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
