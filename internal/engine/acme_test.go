package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

func TestCertDue(t *testing.T) {
	now := time.Now()
	s := model.Site{Domain: "a.test", Aliases: []string{"www.a.test"}, HTTPS: true, TLSMode: model.TLSModeLocal, Status: model.StatusActive}
	if !certDue(s, now) {
		t.Error("new site without a certificate is not due")
	}
	s.TLS = model.SiteTLS{Status: model.TLSActive, Domains: []string{"a.test", "www.a.test"}, Expires: now.Add(60 * 24 * time.Hour)}
	if certDue(s, now) {
		t.Error("fresh certificate is due")
	}
	s.TLS.Expires = now.Add(10 * 24 * time.Hour)
	if !certDue(s, now) {
		t.Error("expiring certificate is not due")
	}
	s.TLS.Expires = now.Add(60 * 24 * time.Hour)
	s.Aliases = append(s.Aliases, "shop.a.test")
	if !certDue(s, now) {
		t.Error("certificate missing a new alias is not due")
	}
	s.TLS.NextTry = now.Add(time.Hour)
	if certDue(s, now) {
		t.Error("due despite backing off")
	}
	s.TLS.NextTry = time.Time{}
	for _, f := range []func(*model.Site){
		func(x *model.Site) { x.TLSMode = model.TLSModeProxy },
		func(x *model.Site) { x.HTTPS = false },
		func(x *model.Site) { x.Halyard.Enabled = true },
		func(x *model.Site) { x.Status = model.StatusProvisioning },
	} {
		c := s
		f(&c)
		if certDue(c, now) {
			t.Errorf("due for %+v", c)
		}
	}
}

// TestIssueCertPebble gets a real certificate from Pebble, Let's Encrypt's
// test CA. Run it with PEBBLE=https://127.0.0.1:14000/dir after starting
// pebble (httpPort 5002) and pointing pebble.berth.test at 127.0.0.1.
func TestIssueCertPebble(t *testing.T) {
	dir := os.Getenv("PEBBLE")
	if dir == "" {
		t.Skip("PEBBLE not set")
	}
	e := testEngine(t)
	e.Cfg.DryRun = false
	e.ACMEDirectory = dir
	e.ACMEHTTP = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	e.SkipACMEPreflight = true
	// Pebble checks http://<name>:5002/.well-known/acme-challenge/<token>.
	ln, err := net.Listen("tcp", "127.0.0.1:5002")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.FileServer(http.Dir(e.Cfg.ACMEDir))}
	go srv.Serve(ln)
	defer srv.Close()
	ctx := context.Background()
	s := model.Site{Name: "pebble", Domain: "pebble.berth.test", Aliases: []string{"www.pebble.berth.test"}, HTTPS: true, TLSMode: model.TLSModeLocal}
	var log []string
	got, err := e.IssueCert(ctx, s, func(f string, a ...any) { log = append(log, f) })
	if err != nil {
		t.Fatalf("issue: %v\n%s", err, strings.Join(log, "\n"))
	}
	if got.Status != model.TLSActive || len(got.Domains) != 2 || got.Expires.Before(time.Now()) {
		t.Fatalf("tls %+v", got)
	}
	b, err := os.ReadFile(e.Paths().CertFile("pebble"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || strings.Join(leaf.DNSNames, ",") != "pebble.berth.test,www.pebble.berth.test" {
		t.Fatalf("leaf %v %v", leaf.DNSNames, err)
	}
	if fi, err := os.Stat(e.Paths().KeyFile("pebble")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key %v %v", fi, err)
	}
	// A second certificate reuses the registered account.
	if _, err := e.IssueCert(ctx, s, func(string, ...any) {}); err != nil {
		t.Fatalf("second issue: %v", err)
	}
}
