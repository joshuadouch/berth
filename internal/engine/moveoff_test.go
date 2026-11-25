package engine

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

// fakeDNS stands in for the Cloudflare plugin.
type fakeDNS struct {
	changes []string
	points  bool
}

func (f *fakeDNS) SiteChanged(ctx context.Context, old, cur *model.Site, logf func(string, ...any)) {
	f.changes = append(f.changes, old.Name+":"+map[bool]string{true: "halyard", false: "direct"}[old.Halyard.Enabled]+
		"->"+map[bool]string{true: "halyard", false: "direct"}[cur.Halyard.Enabled])
}

func (f *fakeDNS) PointsHere(ctx context.Context, s model.Site) bool { return f.points }

func TestMoveOffHalyard(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	f := &fakeHalyard{hosts: map[string]map[string]any{}, certs: map[string][]string{}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	e.HalyardHTTP = srv.Client()
	dns := &fakeDNS{}
	e.Hooks = dns
	st, _ := e.Store.Settings(ctx)
	st.Halyard = model.HalyardSettings{URL: srv.URL, Token: "hlt_test", PoolID: "p1", VIPID: "v1", Exposure: "public", AutoCert: true}
	st.Brand.IPv4 = "203.0.113.10"
	_ = e.Store.SetSettings(ctx, st)
	for _, n := range []string{"demo", "nocert"} {
		s := model.Site{ID: n, Name: n, Type: model.TypePHP, Domain: n + ".test", PHPVersion: "8.3", Enabled: true, HTTPS: true, Status: model.StatusActive}
		if n == "demo" {
			// It already has a certificate of its own, so the move needs no CA.
			s.TLS = model.SiteTLS{Status: model.TLSActive, Domains: []string{"demo.test"}, Expires: time.Now().Add(60 * 24 * time.Hour)}
		}
		_ = e.Store.SaveSite(ctx, s)
		if _, err := e.LinkHalyard(ctx, n, model.HalyardLink{}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	dns.changes = nil
	logf := func(string, ...any) {}

	// No certificate to be had (dry run): the site stays on Halyard as it was.
	if err := e.moveOffHalyard(ctx, "nocert", logf); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("move without certificate: %v", err)
	}
	s, _ := e.Store.Site(ctx, "nocert")
	if !s.Halyard.Enabled || s.TLSMode != model.TLSModeProxy || !s.Halyard.MovedAt.IsZero() || len(dns.changes) != 0 {
		t.Fatalf("not reverted: %+v tls=%q dns=%v", s.Halyard, s.TLSMode, dns.changes)
	}

	// DNS not kept by a plugin: moved, but Halyard waits for a person.
	if err := e.moveOffHalyard(ctx, "demo", logf); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Store.Site(ctx, "demo")
	if s.Halyard.Enabled || s.Halyard.HostID == "" || s.TLSMode != model.TLSModeLocal || !s.ServesTLS() || !s.Halyard.RemoveAt.IsZero() {
		t.Fatalf("after move: %+v tls=%q", s.Halyard, s.TLSMode)
	}
	if strings.Join(dns.changes, ",") != "demo:halyard->direct" {
		t.Fatalf("dns changes %v", dns.changes)
	}
	e.removeMovedHalyard(ctx, time.Now().Add(time.Hour))
	if len(f.hosts) != 2 {
		t.Fatal("host removed before DNS was pointed")
	}

	// Moving again is a no-op; publishing again would undo it.
	if err := e.moveOffHalyard(ctx, "demo", logf); err != nil {
		t.Fatal(err)
	}

	// With DNS kept by a plugin the host goes after the grace period.
	s.Halyard.Enabled = true
	s.TLSMode = model.TLSModeProxy
	_ = e.Store.SaveSite(ctx, s)
	dns.points = true
	if err := e.moveOffHalyard(ctx, "demo", logf); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Store.Site(ctx, "demo")
	if s.Halyard.RemoveAt.IsZero() || s.Halyard.RemoveAt.Before(time.Now().Add(halyardGrace-time.Minute)) {
		t.Fatalf("remove at %v", s.Halyard.RemoveAt)
	}
	e.removeMovedHalyard(ctx, time.Now())
	if len(f.hosts) != 2 {
		t.Fatal("host removed during the grace period")
	}
	e.removeMovedHalyard(ctx, time.Now().Add(halyardGrace+time.Minute))
	s, _ = e.Store.Site(ctx, "demo")
	if len(f.hosts) != 1 || s.Halyard.HostID != "" || s.Halyard.MovedAt.IsZero() {
		t.Fatalf("after grace: hosts %d link %+v", len(f.hosts), s.Halyard)
	}

}
