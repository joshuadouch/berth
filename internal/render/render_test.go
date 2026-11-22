package render

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

func testSites(t *testing.T) []model.Site {
	sites := []model.Site{
		{Name: "blog", Type: model.TypeWordPress, Domain: "blog.example.com", Aliases: []string{"www.blog.example.com"}, RedirectAliases: true, PHPVersion: "8.3", HTTPS: true, Enabled: true},
		{Name: "app", Type: model.TypePHPDB, Domain: "app.example.com", PHPVersion: "8.2", DocRoot: "web", FrontController: true, Enabled: true},
		{Name: "static", Type: model.TypePHP, Domain: "static.example.com", PHPVersion: "8.3", Enabled: true,
			NginxCustom: "location = /old {\r\n  return 301 /new;\r\n}\n\nadd_header X-Test yes;"},
		{Name: "off", Type: model.TypePHP, Domain: "off.example.com", PHPVersion: "8.3", Enabled: false},
		{Name: "shop", Type: model.TypeWordPress, Domain: "shop.test", Aliases: []string{"www.shop.test"}, RedirectAliases: true, PHPVersion: "8.3",
			HTTPS: true, TLSMode: model.TLSModeLocal, Enabled: true, TLS: model.SiteTLS{Status: model.TLSActive, Domains: []string{"shop.test", "www.shop.test"}}},
		{Name: "newbie", Type: model.TypePHP, Domain: "newbie.test", PHPVersion: "8.3", HTTPS: true, TLSMode: model.TLSModeLocal, Enabled: true},
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
		PoolDir: filepath.Join(dir, "php", "{v}"), SocketTmpl: "/run/php/berth-{site}.sock", IPv6: false,
		ACMEDir: filepath.Join(dir, "acme"), CertDir: filepath.Join(dir, "certs")}
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
		if s.ServesTLS() {
			writeTestCert(t, p.CertFile(s.Name), p.KeyFile(s.Name))
		}
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

func TestLocalTLS(t *testing.T) {
	dir := t.TempDir()
	p := paths(dir)
	out, err := Render(p, model.Settings{ProxyOnly: true}, testSites(t))
	if err != nil {
		t.Fatal(err)
	}
	http := out.Files[filepath.Join(p.NginxConf, "http.conf")]
	if !strings.Contains(http, "ssl_reject_handshake on;") {
		t.Error("no default HTTPS server")
	}
	shop := out.Files[filepath.Join(p.NginxConf, "sites", "shop.conf")]
	for _, want := range []string{
		"listen 443 ssl http2;", "ssl_certificate " + p.CertFile("shop") + ";", "location ^~ /.well-known/acme-challenge/",
		"return 301 https://shop.test$request_uri;", "server_name shop.test www.shop.test;",
	} {
		if !strings.Contains(shop, want) {
			t.Errorf("shop.conf lacks %q", want)
		}
	}
	if strings.Contains(shop, "berth_from_proxy = 0") {
		t.Error("a site Berth serves directly refuses direct visitors")
	}
	// Waiting for its certificate: plain HTTP with the challenge location.
	newbie := out.Files[filepath.Join(p.NginxConf, "sites", "newbie.conf")]
	if strings.Contains(newbie, "listen 443") || !strings.Contains(newbie, "acme-challenge") || strings.Contains(newbie, "return 301 https") {
		t.Errorf("newbie.conf:\n%s", newbie)
	}
	// Proxied sites are unchanged.
	if blog := out.Files[filepath.Join(p.NginxConf, "sites", "blog.conf")]; strings.Contains(blog, "443") || strings.Contains(blog, "acme") {
		t.Error("proxied site got local TLS")
	}
}

// writeTestCert writes a self-signed certificate for nginx -t.
func writeTestCert(t *testing.T, certFile, keyFile string) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	_ = os.MkdirAll(filepath.Dir(certFile), 0o755)
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
}
