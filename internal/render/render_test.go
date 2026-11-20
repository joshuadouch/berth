package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
)

func testSites(t *testing.T) []model.Site {
	sites := []model.Site{
		{Name: "blog", Type: model.TypeWordPress, Domain: "blog.example.com", Aliases: []string{"www.blog.example.com"}, RedirectAliases: true, PHPVersion: "8.3", HTTPS: true, Enabled: true},
		{Name: "app", Type: model.TypePHPDB, Domain: "app.example.com", PHPVersion: "8.2", DocRoot: "web", FrontController: true, Enabled: true},
		{Name: "static", Type: model.TypePHP, Domain: "static.example.com", PHPVersion: "8.3", Enabled: true,
			NginxCustom: "location = /old {\r\n  return 301 /new;\r\n}\n\nadd_header X-Test yes;"},
		{Name: "off", Type: model.TypePHP, Domain: "off.example.com", PHPVersion: "8.3", Enabled: false},
	}
	for i := range sites {
		if err := sites[i].Normalize(); err != nil {
			t.Fatal(err)
		}
	}
	return sites
}

func paths(dir string) Paths {
	return Paths{SitesDir: filepath.Join(dir, "srv"), LogDir: filepath.Join(dir, "log"), NginxConf: filepath.Join(dir, "nginx"),
		PoolDir: filepath.Join(dir, "php", "{v}"), SocketTmpl: "/run/php/berth-{site}.sock", IPv6: false}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	p := paths(dir)
	st := model.Settings{TrustedProxies: []string{"10.0.30.11", "10.0.40.0/24"}, ProxyOnly: true}
	out, err := Render(p, st, testSites(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.PHPVersions, ",") != "8.2,8.3" {
		t.Fatalf("versions %v", out.PHPVersions)
	}
	http := out.Files[filepath.Join(p.NginxConf, "http.conf")]
	for _, want := range []string{"set_real_ip_from 10.0.30.11;", "set_real_ip_from 10.0.40.0/24;", "10.0.40.0/24 1;", "real_ip_header X-Real-IP;", `"1:https" on;`} {
		if !strings.Contains(http, want) {
			t.Errorf("http.conf lacks %q", want)
		}
	}
	blog := out.Files[filepath.Join(p.NginxConf, "sites", "blog.conf")]
	for _, want := range []string{
		"server_name www.blog.example.com;", "return 301 $berth_scheme://blog.example.com$request_uri;", "server_name blog.example.com;",
		"try_files $uri $uri/ /index.php?$args;", "wp-content/uploads", "fastcgi_pass unix:/run/php/berth-blog.sock;",
		"if ($berth_from_proxy = 0)", "root " + filepath.Join(dir, "srv", "blog", "public") + ";",
	} {
		if !strings.Contains(blog, want) {
			t.Errorf("blog.conf lacks %q", want)
		}
	}
	app := out.Files[filepath.Join(p.NginxConf, "sites", "app.conf")]
	if !strings.Contains(app, "root "+filepath.Join(dir, "srv", "app", "public", "web")+";") {
		t.Error("app doc root not used")
	}
	static := out.Files[filepath.Join(p.NginxConf, "sites", "static.conf")]
	if !strings.Contains(static, "try_files $uri $uri/ =404;") {
		t.Error("static site should not use a front controller")
	}
	if off := out.Files[filepath.Join(p.NginxConf, "sites", "off.conf")]; !strings.Contains(off, "return 503") || strings.Contains(off, "fastcgi_pass") {
		t.Error("disabled site should only return 503")
	}
	pool := out.Files[filepath.Join(dir, "php", "8.2", "berth-app.conf")]
	for _, want := range []string{"[berth-app]", "user = site_app", "listen = /run/php/berth-app.sock",
		"php_admin_value[open_basedir] = " + filepath.Join(dir, "srv", "app") + "/:", "pm = ondemand"} {
		if !strings.Contains(pool, want) {
			t.Errorf("pool lacks %q", want)
		}
	}
	if strings.Contains(pool, "#") {
		t.Error("PHP-FPM pools must use ; comments")
	}

	dup := testSites(t)
	dup[1].Aliases = []string{"blog.example.com"}
	if _, err := Render(p, st, dup); err == nil {
		t.Error("a hostname on two sites must fail")
	}
}

// TestNginxAccepts runs nginx -t on the rendered config when nginx is installed.
func TestCustomNginx(t *testing.T) {
	sites := testSites(t)
	v := Vhost(paths(t.TempDir()), model.Settings{}, sites[2])
	if !strings.Contains(v, "    location = /old {\n      return 301 /new;\n    }\n\n    add_header X-Test yes;\n") {
		t.Fatalf("custom block missing:\n%s", v)
	}
	if strings.Index(v, "location = /old") > strings.Index(v, "location / {") {
		t.Fatal("custom block should come before Berth's locations")
	}
}

func TestNginxAccepts(t *testing.T) {
	bin, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx not installed")
	}
	dir := t.TempDir()
	p := paths(dir)
	out, err := Render(p, model.Settings{TrustedProxies: []string{"10.0.30.11"}}, testSites(t))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range out.Files {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range testSites(t) {
		_ = os.MkdirAll(filepath.Join(p.LogDir, s.Name), 0o755)
	}
	conf := filepath.Join(dir, "nginx.conf")
	main := "pid " + filepath.Join(dir, "nginx.pid") + ";\nerror_log stderr;\nevents {}\nhttp {\n" +
		"  access_log off;\n  client_body_temp_path " + dir + ";\n  proxy_temp_path " + dir + ";\n  fastcgi_temp_path " + dir + ";\n" +
		"  uwsgi_temp_path " + dir + ";\n  scgi_temp_path " + dir + ";\n" +
		"  include " + filepath.Join(p.NginxConf, "http.conf") + ";\n  include " + filepath.Join(p.NginxConf, "sites", "*.conf") + ";\n}\n"
	if err := os.WriteFile(conf, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-t", "-q", "-p", dir, "-c", conf, "-e", "stderr")
	if b, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(b), "Permission denied") || strings.Contains(string(b), "Address family") {
			t.Skip(string(b))
		}
		t.Fatalf("nginx -t: %v\n%s", err, b)
	}
}
