package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
)

// fakeHalyard is a tiny in-memory stand-in for the Halyard API.
type fakeHalyard struct {
	mu    sync.Mutex
	hosts map[string]map[string]any
	pool  map[string]any
	certs map[string][]string
	n     int
}

func (f *fakeHalyard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer hlt_test" {
		w.WriteHeader(401)
		return
	}
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	path := r.URL.Path
	switch {
	case path == "/api/vips":
		send([]map[string]any{{"id": "v1", "address": "10.0.0.10/24"}})
	case path == "/api/pools/p1" && f.pool != nil:
		if r.Method == "PUT" {
			f.pool = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&f.pool)
		}
		send(f.pool)
	case path == "/api/pools" && r.Method == "GET":
		send([]map[string]any{{"id": "p1", "name": "berth"}})
	case path == "/api/certs" && r.Method == "GET":
		out := []map[string]any{}
		for id, d := range f.certs {
			hosts := 0
			for _, h := range f.hosts {
				for _, n := range h["hostnames"].([]any) {
					for _, x := range d {
						if x == n {
							hosts++
						}
					}
				}
			}
			out = append(out, map[string]any{"id": id, "domains": d, "status": "valid", "hosts": hosts})
		}
		send(out)
	case path == "/api/certs" && r.Method == "POST":
		var req struct{ Domains []string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.n++
		id := "c" + string(rune('0'+f.n))
		f.certs[id] = req.Domains
		send(map[string]any{"id": id, "domains": req.Domains})
	case strings.HasPrefix(path, "/api/certs/") && r.Method == "DELETE":
		delete(f.certs, strings.TrimPrefix(path, "/api/certs/"))
		send(map[string]any{"ok": true})
	case path == "/api/hosts" && r.Method == "GET":
		out := []map[string]any{}
		for _, h := range f.hosts {
			out = append(out, h)
		}
		send(out)
	case path == "/api/hosts" && r.Method == "POST", strings.HasPrefix(path, "/api/hosts/") && r.Method == "PUT":
		var h map[string]any
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&h); err != nil {
			w.WriteHeader(400)
			return
		}
		if _, ok := h["id"]; ok {
			w.WriteHeader(400)
			send(map[string]string{"error": "unknown field id"})
			return
		}
		id := strings.TrimPrefix(path, "/api/hosts/")
		if r.Method == "POST" {
			f.n++
			id = "h" + string(rune('0'+f.n))
		} else if _, ok := f.hosts[id]; !ok {
			w.WriteHeader(404)
			return
		}
		f.hosts[id] = h
		h["id"] = id
		send(h)
	case strings.HasPrefix(path, "/api/hosts/"):
		id := strings.TrimPrefix(path, "/api/hosts/")
		h, ok := f.hosts[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.Method == "DELETE" {
			delete(f.hosts, id)
			send(map[string]any{"ok": true})
			return
		}
		cp := map[string]any{}
		for k, v := range h {
			cp[k] = v
		}
		send(cp)
	default:
		w.WriteHeader(404)
	}
}

func TestHalyardSync(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	f := &fakeHalyard{hosts: map[string]map[string]any{}, certs: map[string][]string{}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	e.HalyardHTTP = srv.Client()

	st, _ := e.Store.Settings(ctx)
	st.Halyard = model.HalyardSettings{URL: srv.URL, Token: "hlt_test", PoolID: "p1", VIPID: "v1", Exposure: "public", AutoCert: true}
	if err := e.Store.SetSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	info := e.HalyardInfo(ctx)
	if !info.Configured || info.Error != "" || !info.PoolOK || len(info.VIPs) != 1 {
		t.Fatalf("info %+v", info)
	}

	s := model.Site{ID: "demo", Name: "demo", Type: model.TypePHP, Domain: "demo.test", PHPVersion: "8.3", Enabled: true, HTTPS: true,
		Status: model.StatusActive}
	if err := e.Store.SaveSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	s, err := e.LinkHalyard(ctx, "demo", model.HalyardLink{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if s.Halyard.HostID == "" || s.Halyard.CertID == "" || s.Halyard.VIPID != "v1" || s.Halyard.Exposure != "public" {
		t.Fatalf("link %+v", s.Halyard)
	}
	h := f.hosts[s.Halyard.HostID]
	if h["https_port"].(float64) != 443 || h["force_https"] != true || h["publish_dns"] != true {
		t.Fatalf("host %+v", h)
	}
	routes := h["routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["pool_id"] != "p1" {
		t.Fatalf("routes %+v", routes)
	}

	// Edits made on Halyard survive; a domain change moves the host and
	// replaces Berth's certificate.
	f.hosts[s.Halyard.HostID]["hsts"] = true
	f.hosts[s.Halyard.HostID]["allow_cidrs"] = []any{"10.0.0.0/8"}
	oldCert := s.Halyard.CertID
	s.Domain = "new.test"
	if err := e.Store.SaveSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	e.syncHalyard(ctx, &s, func(string, ...any) {})
	s, _ = e.Store.Site(ctx, "demo")
	h = f.hosts[s.Halyard.HostID]
	if s.Halyard.Error != "" || h["hsts"] != true || h["hostnames"].([]any)[0] != "new.test" || len(f.hosts) != 1 {
		t.Fatalf("after rename: %+v %+v", s.Halyard, h)
	}
	if _, ok := f.certs[oldCert]; ok || s.Halyard.CertID == oldCert {
		t.Fatalf("old certificate kept: %+v", f.certs)
	}

	// A host deleted on Halyard is recreated.
	delete(f.hosts, s.Halyard.HostID)
	e.syncHalyard(ctx, &s, func(string, ...any) {})
	s, _ = e.Store.Site(ctx, "demo")
	if len(f.hosts) != 1 || f.hosts[s.Halyard.HostID] == nil {
		t.Fatalf("not recreated: %+v", s.Halyard)
	}

	// Unlink removes the host and the unused certificate.
	s, err = e.UnlinkHalyard(ctx, "demo", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.hosts) != 0 || len(f.certs) != 0 || s.Halyard.Enabled {
		t.Fatalf("after unlink: hosts %v certs %v link %+v", f.hosts, f.certs, s.Halyard)
	}

	// A bad token is recorded on the site, not fatal.
	st.Halyard.Token = "wrong"
	_ = e.Store.SetSettings(ctx, st)
	s.Halyard.Enabled = true
	e.syncHalyard(ctx, &s, func(string, ...any) {})
	s, _ = e.Store.Site(ctx, "demo")
	if !strings.Contains(s.Halyard.Error, "401") {
		t.Fatalf("error %q", s.Halyard.Error)
	}
}

func TestWithDefaultRoute(t *testing.T) {
	in := []any{
		map[string]any{"path_prefix": "/api", "pool_id": "other"},
		map[string]any{"path_prefix": "", "pool_id": "old"},
	}
	out := withDefaultRoute(in, "p1")
	if len(out) != 2 || out[0].(map[string]any)["pool_id"] != "other" || out[1].(map[string]any)["pool_id"] != "p1" {
		t.Fatalf("%+v", out)
	}
	if out := withDefaultRoute(nil, "p1"); len(out) != 1 {
		t.Fatalf("%+v", out)
	}
}

func TestHalyardTakeover(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	f := &fakeHalyard{hosts: map[string]map[string]any{}, certs: map[string][]string{"cold": {"shop.test", "www.shop.test"}}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	e.HalyardHTTP = srv.Client()
	st, _ := e.Store.Settings(ctx)
	st.Halyard = model.HalyardSettings{URL: srv.URL, Token: "hlt_test", PoolID: "p1", VIPID: "v1", Exposure: "public", AutoCert: true}
	_ = e.Store.SetSettings(ctx, st)
	old := func(names ...any) map[string]any {
		return map[string]any{"id": "old", "hostnames": names, "vip_id": "v9", "exposure": "tunnel", "publish_dns": false,
			"hsts": true, "routes": []any{map[string]any{"path_prefix": "/api", "pool_id": "api"}, map[string]any{"path_prefix": "", "pool_id": "oldweb"}}}
	}
	s := model.Site{ID: "shop", Name: "shop", Type: model.TypePHP, Domain: "shop.test", Aliases: []string{"www.shop.test"},
		PHPVersion: "8.3", Enabled: true, HTTPS: true, Status: model.StatusActive}
	_ = e.Store.SaveSite(ctx, s)

	// Extra names on the existing host: refused, nothing changed.
	f.hosts["old"] = old("shop.test", "WWW.shop.test", "blog.shop.test")
	if _, err := e.LinkHalyard(ctx, "shop", model.HalyardLink{}, "t"); err == nil || !strings.Contains(err.Error(), "blog.shop.test") {
		t.Fatalf("extra names: %v", err)
	}
	// Fewer names: refused.
	f.hosts["old"] = old("shop.test")
	if _, err := e.LinkHalyard(ctx, "shop", model.HalyardLink{}, "t"); err == nil || !strings.Contains(err.Error(), "does not serve www.shop.test") {
		t.Fatalf("missing names: %v", err)
	}
	if f.hosts["old"]["routes"].([]any)[1].(map[string]any)["pool_id"] != "oldweb" || len(f.hosts) != 1 {
		t.Fatalf("changed on error: %+v", f.hosts)
	}

	// Exactly the same names (any case): taken over, only the backend moves.
	f.hosts["old"] = old("WWW.shop.test", "shop.test")
	s, err := e.LinkHalyard(ctx, "shop", model.HalyardLink{}, "t")
	if err != nil {
		t.Fatal(err)
	}
	h := f.hosts["old"]
	routes := h["routes"].([]any)
	if len(f.hosts) != 1 || s.Halyard.HostID != "old" || len(routes) != 2 || routes[0].(map[string]any)["pool_id"] != "api" || routes[1].(map[string]any)["pool_id"] != "p1" {
		t.Fatalf("takeover: %+v %+v", s.Halyard, h)
	}
	if h["vip_id"] != "v9" || h["exposure"] != "tunnel" || h["publish_dns"] != false || h["hsts"] != true || s.Halyard.CertID != "" || len(f.certs) != 1 {
		t.Fatalf("settings not kept: %+v %+v %v", s.Halyard, h, f.certs)
	}
}

func TestHalyardFixesHealthHost(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	f := &fakeHalyard{hosts: map[string]map[string]any{}, certs: map[string][]string{},
		pool: map[string]any{"id": "p1", "name": "berth", "health": map[string]any{"enabled": true, "type": "http", "path": "/berth-health", "host_header": ""}}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	e.HalyardHTTP = srv.Client()
	st, _ := e.Store.Settings(ctx)
	st.Halyard = model.HalyardSettings{URL: srv.URL, Token: "hlt_test", PoolID: "p1"}
	_ = e.Store.SetSettings(ctx, st)
	if info := e.HalyardInfo(ctx); !info.PoolOK {
		t.Fatalf("%+v", info)
	}
	if h := f.pool["health"].(map[string]any); h["host_header"] != "berth-health" || f.pool["name"] != "berth" {
		t.Fatalf("pool %+v", f.pool)
	}
	if _, ok := f.pool["id"]; ok {
		t.Fatal("id sent in PUT")
	}
}
