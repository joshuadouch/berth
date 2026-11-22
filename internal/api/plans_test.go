package api

import (
	"context"
	"strings"
	"testing"
)

func TestPlans(t *testing.T) {
	ts, st := testServer(t)
	admin := &client{t: t, ts: ts}
	admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false)
	code, plan := admin.do("POST", "/api/plans", map[string]any{"name": "Starter", "max_sites": 1, "max_domains": 2, "memory_mb": 128, "workers": 3, "upload_mb": 32, "no_sftp": true}, true)
	if code != 200 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	if code, _ := admin.do("POST", "/api/plans", map[string]any{"name": "Bad", "workers": -1}, true); code != 400 {
		t.Errorf("negative limit: %d", code)
	}
	_, out := admin.do("POST", "/api/accounts", map[string]any{"name": "Acme", "plan_id": plan["id"], "user": map[string]string{"username": "acme", "password": "acme acme acme 1"}}, true)
	acct := out["id"].(string)
	if out["plan_id"] != plan["id"] {
		t.Fatalf("account plan: %v", out)
	}
	cust := &client{t: t, ts: ts}
	cust.do("POST", "/api/login", map[string]string{"username": "acme", "password": "acme acme acme 1"}, false)
	if code, _ := cust.do("GET", "/api/plans", nil, true); code != 403 {
		t.Errorf("customer read plans: %d", code)
	}

	site := map[string]any{"type": "php", "domain": "a.test", "aliases": []string{"www.a.test", "b.a.test"}, "php_version": "8.3", "enabled": true,
		"upload_mb": 500, "sftp": map[string]any{"enabled": true}}
	if code, out := cust.do("POST", "/api/sites", site, true); code != 400 || !strings.Contains(out["error"].(string), "2 domains") {
		t.Fatalf("too many domains: %d %v", code, out)
	}
	site["aliases"] = []string{"www.a.test"}
	code, out = cust.do("POST", "/api/sites", site, true)
	if code != 200 {
		t.Fatalf("site: %d %v", code, out)
	}
	s := out["site"].(map[string]any)
	if s["memory_mb"].(float64) != 128 || s["max_children"].(float64) != 3 || s["upload_mb"].(float64) != 32 || s["sftp"].(map[string]any)["enabled"] != false {
		t.Fatalf("plan not applied: %v", s)
	}
	waitActive(t, cust, "atest")
	site["domain"], site["aliases"] = "b.test", []string{}
	if code, out := cust.do("POST", "/api/sites", site, true); code != 400 || !strings.Contains(out["error"].(string), "allows 1 site") {
		t.Fatalf("second site: %d %v", code, out)
	}
	// A bigger plan lifts the limits; deleting a plan in use is refused.
	if code, _ := admin.do("PUT", "/api/plans/"+plan["id"].(string), map[string]any{"name": "Starter", "max_sites": 2, "memory_mb": 64, "allow_shell": true}, true); code != 200 {
		t.Fatal("update plan")
	}
	if code, _ := admin.do("DELETE", "/api/plans/"+plan["id"].(string), nil, true); code != 400 {
		t.Errorf("deleted a plan in use: %d", code)
	}
	if code, out := cust.do("POST", "/api/sites", site, true); code != 200 {
		t.Fatalf("site within new plan: %d %v", code, out)
	}
	var acctView map[string]any
	_, acctView = admin.do("GET", "/api/accounts/"+acct, nil, true)
	if u := acctView["usage"].(map[string]any); u["sites"].(float64) != 2 || u["plan"].(map[string]any)["name"] != "Starter" {
		t.Fatalf("usage: %v", u)
	}
	// Taking the plan away removes the limits.
	if code, _ := admin.do("PUT", "/api/accounts/"+acct, map[string]any{"name": "Acme", "plan_id": ""}, true); code != 200 {
		t.Fatal("clear plan")
	}
	if _, ok := st.AccountPlan(context.Background(), acct); ok {
		t.Fatal("plan still set")
	}
}
