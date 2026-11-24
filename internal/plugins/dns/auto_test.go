package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/store"
)

// fakePDNS keeps zones in memory, enough for zone creation.
type fakePDNS struct {
	mu    sync.Mutex
	zones map[string]Zone
}

func (f *fakePDNS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/localhost")
	switch {
	case r.Method == "GET" && path == "/zones":
		out := []Zone{}
		for _, z := range f.zones {
			out = append(out, Zone{Name: z.Name, Kind: z.Kind})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == "POST" && path == "/zones":
		var z Zone
		_ = json.NewDecoder(r.Body).Decode(&z)
		f.zones[z.Name] = z
		w.WriteHeader(201)
	case r.Method == "GET" && strings.HasPrefix(path, "/zones/"):
		z, ok := f.zones[strings.TrimPrefix(path, "/zones/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(z)
	case r.Method == "PATCH":
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func TestPlanGateAndAutoZone(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pd := &fakePDNS{zones: map[string]Zone{}}
	srv := httptest.NewServer(pd)
	t.Cleanup(srv.Close)

	_ = st.SaveAccount(ctx, model.Account{ID: "acc-admin", Name: "Admin"})
	if err := st.SaveUser(ctx, model.User{ID: "u1", Username: "admin", Role: model.RoleAdmin, AccountID: "acc-admin", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePlan(ctx, model.Plan{ID: "starter", Name: "Starter", DNSZones: 2}); err != nil {
		t.Fatal(err)
	}
	_ = st.SaveAccount(ctx, model.Account{ID: "acc-cust", Name: "Cust", PlanID: "starter"})
	_ = st.SaveAccount(ctx, model.Account{ID: "acc-none", Name: "No plan"})

	p := New()
	reg := plugin.NewRegistry(st, nil, p)
	if _, err := reg.Save(ctx, ID, true, plugin.Config{"api_url": srv.URL, "api_key": "k", "nameservers": "ns1.ratlan.net, ns2.ratlan.net", "ipv4": "203.0.113.10"}); err != nil {
		t.Fatal(err)
	}

	// The plan includes DNS, so the customer has the plugin without being picked.
	if !reg.ActiveFor(ctx, ID, "acc-cust") {
		t.Fatal("plan with DNS domains does not open the DNS plugin")
	}
	if reg.ActiveFor(ctx, ID, "acc-none") {
		t.Fatal("account without a plan got the DNS plugin")
	}

	// A new customer site on a fresh domain gets its zone, with records.
	site := model.Site{Name: "shop", AccountID: "acc-cust", Domain: "www.shop.co.uk", Status: model.StatusActive}
	if err := st.SaveSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }
	reg.SiteChanged(ctx, nil, &site, logf)
	o := p.owners(ctx)
	if o["shop.co.uk"].Account != "acc-cust" {
		t.Fatalf("zone not added for the new site: %v %v", o, logs)
	}
	z, ok := pd.zones["shop.co.uk."]
	if !ok {
		t.Fatalf("zone not on the nameserver: %v", pd.zones)
	}
	var hasNS, hasA bool
	for _, s := range z.RRSets {
		hasNS = hasNS || s.Type == "NS" && len(s.Records) == 2 && s.Records[0].Content == "ns1.ratlan.net."
		hasA = hasA || s.Type == "A" && s.Name == "www.shop.co.uk." && s.Records[0].Content == "203.0.113.10"
	}
	if !hasNS || !hasA {
		t.Fatalf("zone records: %+v", z.RRSets)
	}

	// A second site in the same domain does not make another zone.
	site2 := model.Site{Name: "blog", AccountID: "acc-cust", Domain: "blog.shop.co.uk", Status: model.StatusActive}
	_ = st.SaveSite(ctx, site2)
	reg.SiteChanged(ctx, nil, &site2, logf)
	if len(p.owners(ctx)) != 1 {
		t.Fatalf("zones: %v", p.owners(ctx))
	}

	// The admin's own sites are left alone.
	adminSite := model.Site{Name: "mine", AccountID: "acc-admin", Domain: "josh.example", Status: model.StatusActive}
	_ = st.SaveSite(ctx, adminSite)
	reg.SiteChanged(ctx, nil, &adminSite, logf)
	if _, ok := p.owners(ctx)["josh.example"]; ok {
		t.Fatal("zone made for the admin's own site")
	}

	// Plan limit: Starter allows two zones.
	for i, d := range []string{"two.com", "three.com"} {
		s := model.Site{Name: "s" + d, AccountID: "acc-cust", Domain: d, Status: model.StatusActive}
		_ = st.SaveSite(ctx, s)
		reg.SiteChanged(ctx, nil, &s, logf)
		_, ok := p.owners(ctx)[d]
		if ok != (i == 0) {
			t.Fatalf("%s zone made = %v (limit is 2)", d, ok)
		}
	}
}
