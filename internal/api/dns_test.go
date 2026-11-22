package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestDNSPlugin runs against real PowerDNS servers when PDNS_PRIMARY (and
// optionally PDNS_SECONDARY) are set, e.g.
//
//	PDNS_PRIMARY=http://127.0.0.1:8091,pkey PDNS_SECONDARY=http://127.0.0.1:8092,skey,127.0.0.1:5301
func TestDNSPlugin(t *testing.T) {
	prim := strings.Split(os.Getenv("PDNS_PRIMARY"), ",")
	if len(prim) != 2 {
		t.Skip("PDNS_PRIMARY is not set")
	}
	ts, _ := testServer(t)
	admin := &client{t: t, ts: ts}
	admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false)
	_, out := admin.do("POST", "/api/accounts", map[string]any{"name": "Acme", "user": map[string]string{"username": "acme", "password": "acme acme acme 1"}}, true)
	acct := out["id"].(string)
	_, out = admin.do("POST", "/api/accounts", map[string]any{"name": "Other", "user": map[string]string{"username": "other", "password": "other other other 1"}}, true)
	other := out["id"].(string)

	cfg := map[string]any{"api_url": prim[0], "api_key": prim[1], "nameservers": "ns1.berth-test.net, ns2.berth-test.net",
		"ipv4": "203.0.113.10", "ipv6": "2001:db8::10"}
	if sec := strings.Split(os.Getenv("PDNS_SECONDARY"), ","); len(sec) == 3 {
		cfg["secondary_url"], cfg["secondary_key"], cfg["primary_ip"] = sec[0], sec[1], sec[2]
	}
	if code, out := admin.do("PUT", "/api/plugins/dns", map[string]any{"enabled": true, "config": cfg}, true); code != 200 {
		t.Fatalf("enable dns: %d %v", code, out)
	}
	if code, out := admin.do("POST", "/api/plugins/dns/test", map[string]any{"config": map[string]any{}}, true); code != 200 {
		t.Fatalf("test: %d %v", code, out)
	}
	if code, _ := admin.do("PUT", "/api/plugins/dns/accounts", map[string]any{"accounts": []string{acct}}, true); code != 200 {
		t.Fatal("dns accounts")
	}

	cust := &client{t: t, ts: ts}
	cust.do("POST", "/api/login", map[string]string{"username": "acme", "password": "acme acme acme 1"}, false)
	oth := &client{t: t, ts: ts}
	oth.do("POST", "/api/login", map[string]string{"username": "other", "password": "other other other 1"}, false)
	if code, _ := oth.do("GET", "/api/p/dns/zones", nil, true); code != 404 {
		t.Fatalf("plugin reached by an account it is off for: %d", code)
	}

	zone := "acme-dns-test.com"
	defer admin.do("DELETE", "/api/p/dns/zones/"+zone, nil, true)
	for _, bad := range []string{"co.uk", "com", "not a domain"} {
		if code, _ := cust.do("POST", "/api/p/dns/zones", map[string]any{"name": bad}, true); code != 400 {
			t.Errorf("zone %q accepted: %d", bad, code)
		}
	}
	if code, out := cust.do("POST", "/api/p/dns/zones", map[string]any{"name": zone, "account_id": other}, true); code != 200 || out["warning"] != "" {
		t.Fatalf("create zone: %d %v", code, out)
	}
	if code, _ := cust.do("POST", "/api/p/dns/zones", map[string]any{"name": zone}, true); code != 409 {
		t.Errorf("duplicate zone: %d", code)
	}
	code, z := cust.do("GET", "/api/p/dns/zones/"+zone, nil, true)
	if code != 200 || z["account_id"] != acct {
		t.Fatalf("zone (customers cannot give zones away): %d %v", code, z)
	}
	if !hasSet(z, "@", "NS", "ns1.berth-test.net.") || !hasSet(z, "@", "SOA", "") {
		t.Fatalf("zone sets: %v", z["sets"])
	}

	// Records.
	for _, s := range []map[string]any{
		{"name": "mail", "type": "A", "records": []string{"198.51.100.7"}},
		{"name": "@", "type": "MX", "records": []string{"10 mail." + zone}},
		{"name": "@", "type": "TXT", "records": []string{"v=spf1 mx ~all"}},
		{"name": "_sip._tcp", "type": "SRV", "records": []string{"10 5 5060 sip.example.org"}},
		{"name": "@", "type": "CAA", "records": []string{"0 issue letsencrypt.org"}},
	} {
		if code, out := cust.do("PUT", "/api/p/dns/zones/"+zone+"/records", s, true); code != 200 {
			t.Fatalf("put %v: %d %v", s, code, out)
		}
	}
	for _, s := range []map[string]any{
		{"name": "x", "type": "A", "records": []string{"2001:db8::1"}},
		{"name": "@", "type": "NS", "records": []string{"ns.evil.test"}},
		{"name": "@", "type": "SOA", "records": []string{"x"}},
		{"name": "x", "type": "CNAME", "records": []string{"a.test", "b.test"}},
	} {
		if code, _ := cust.do("PUT", "/api/p/dns/zones/"+zone+"/records", s, true); code != 400 {
			t.Errorf("bad set %v accepted: %d", s, code)
		}
	}
	// Rename a set.
	if code, out := cust.do("PUT", "/api/p/dns/zones/"+zone+"/records", map[string]any{"name": "smtp", "type": "A", "records": []string{"198.51.100.7"},
		"old": map[string]any{"name": "mail", "type": "A"}}, true); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	_, z = cust.do("GET", "/api/p/dns/zones/"+zone, nil, true)
	if hasSet(z, "mail", "A", "") || !hasSet(z, "smtp", "A", "198.51.100.7") || !hasSet(z, "@", "TXT", `"v=spf1 mx ~all"`) ||
		!hasSet(z, "_sip._tcp", "SRV", "10 5 5060 sip.example.org.") || !hasSet(z, "@", "CAA", `0 issue "letsencrypt.org"`) {
		t.Fatalf("sets after edits: %v", z["sets"])
	}

	// A new site in the zone gets records; a name that already has one is left.
	cust.do("PUT", "/api/p/dns/zones/"+zone+"/records", map[string]any{"name": "shop", "type": "CNAME", "records": []string{"elsewhere.example.org"}}, true)
	_, out = cust.do("POST", "/api/sites", map[string]any{"type": "php", "domain": zone, "aliases": []string{"www." + zone, "shop." + zone},
		"php_version": "8.3", "enabled": true}, true)
	name := out["site"].(map[string]any)["name"].(string)
	waitActive(t, cust, name)
	_, z = cust.do("GET", "/api/p/dns/zones/"+zone, nil, true)
	if !hasSet(z, "@", "A", "203.0.113.10") || !hasSet(z, "www", "AAAA", "2001:db8::10") || hasSet(z, "shop", "A", "") {
		t.Fatalf("site records: %v", z["sets"])
	}
	if code, out := cust.do("GET", "/api/p/dns/sites/"+name, nil, true); code != 200 || out["zone"] != zone {
		t.Fatalf("site dns: %d %v", code, out)
	}
	if code, _ := oth.do("GET", "/api/p/dns/zones/"+zone, nil, true); code != 404 {
		t.Errorf("other account read the zone: %d", code)
	}

	// Dropping an alias removes its records.
	_, site := cust.do("GET", "/api/sites/"+name, nil, true)
	s := site["site"].(map[string]any)
	s["aliases"] = []string{"shop." + zone}
	if code, out := cust.do("PUT", "/api/sites/"+name, s, true); code != 200 {
		t.Fatalf("update site: %d %v", code, out)
	}
	_, z = cust.do("GET", "/api/p/dns/zones/"+zone, nil, true)
	if hasSet(z, "www", "A", "") || !hasSet(z, "@", "A", "203.0.113.10") {
		t.Fatalf("records after dropping www: %v", z["sets"])
	}

	if _, ok := cfg["secondary_url"]; ok {
		if code, out := admin.do("POST", "/api/p/dns/sync", nil, true); code != 200 {
			t.Fatalf("sync: %d %v", code, out)
		}
	}
	if code, out := cust.do("DELETE", "/api/p/dns/zones/"+zone, nil, true); code != 200 || out["warning"] != "" {
		t.Fatalf("delete zone: %d %v", code, out)
	}
	var zl map[string]any
	_, zl = cust.do("GET", "/api/p/dns/zones", nil, true)
	if len(zl["zones"].([]any)) != 0 {
		t.Fatalf("zones left: %v", zl)
	}
}

func hasSet(z map[string]any, name, typ, content string) bool {
	sets, _ := z["sets"].([]any)
	for _, x := range sets {
		s := x.(map[string]any)
		if s["name"] != name || s["type"] != typ {
			continue
		}
		if content == "" {
			return true
		}
		for _, r := range s["records"].([]any) {
			if r == content {
				return true
			}
		}
	}
	return false
}

func waitActive(t *testing.T, c *client, name string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		_, out := c.do("GET", "/api/sites/"+name, nil, true)
		if s, ok := out["site"].(map[string]any); ok && s["status"] != "provisioning" {
			if s["status"] != "active" {
				t.Fatalf("site %s: %v", name, s["status_error"])
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("site %s never finished", name)
}
