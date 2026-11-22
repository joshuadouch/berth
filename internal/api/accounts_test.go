package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

func TestCustomerAccounts(t *testing.T) {
	ts, st := testServer(t)
	admin := &client{t: t, ts: ts}
	if code, out := admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false); code != 200 || out["role"] != "admin" {
		t.Fatalf("setup: %d %v", code, out)
	}
	site := map[string]any{"type": "php", "domain": "josh.example.com", "php_version": "8.3", "enabled": true}
	if code, out := admin.do("POST", "/api/sites", site, true); code != 200 {
		t.Fatalf("admin site: %d %v", code, out)
	}

	code, out := admin.do("POST", "/api/accounts", map[string]any{"name": "Acme Ltd", "email": "ops@acme.test",
		"user": map[string]string{"username": "acme", "password": "acme acme acme 1"}}, true)
	if code != 200 {
		t.Fatalf("create account: %d %v", code, out)
	}
	acct := out["id"].(string)
	if code, _ := admin.do("POST", "/api/accounts", map[string]any{"name": "Dup", "user": map[string]string{"username": "acme", "password": "acme acme acme 1"}}, true); code != 409 {
		t.Fatalf("duplicate username: %d", code)
	}

	cust := &client{t: t, ts: ts}
	if code, out := cust.do("POST", "/api/login", map[string]string{"username": "acme", "password": "acme acme acme 1"}, false); code != 200 || out["role"] != "customer" {
		t.Fatalf("customer login: %d %v", code, out)
	}
	// Customers cannot reach server-level endpoints.
	for _, p := range []string{"/api/system", "/api/plugins", "/api/accounts", "/api/halyard", "/api/resources"} {
		if code, _ := cust.do("GET", p, nil, true); code != 403 {
			t.Errorf("GET %s as customer: %d", p, code)
		}
	}
	if code, _ := cust.do("PUT", "/api/settings", map[string]any{}, true); code != 403 {
		t.Errorf("PUT settings as customer: %d", code)
	}
	// The admin's site is invisible to the customer.
	for _, m := range []string{"GET", "DELETE"} {
		if code, _ := cust.do(m, "/api/sites/joshexamplecom", nil, true); code != 404 {
			t.Errorf("%s admin site as customer: %d", m, code)
		}
	}
	if code, _ := cust.do("GET", "/api/sites/joshexamplecom/files", nil, true); code != 404 {
		t.Errorf("files of admin site as customer: %d", code)
	}

	// A customer's new site belongs to them, without server-level options.
	mine := map[string]any{"type": "php", "domain": "acme.test", "php_version": "8.3", "enabled": true, "max_children": 50,
		"nginx_custom": "location /x { alias /etc/; }", "halyard": map[string]any{"enabled": true}}
	code, out = cust.do("POST", "/api/sites", mine, true)
	if code != 200 {
		t.Fatalf("customer site: %d %v", code, out)
	}
	s := out["site"].(map[string]any)
	if s["account_id"] != acct || s["nginx_custom"] != nil || s["halyard"].(map[string]any)["enabled"] != false || s["max_children"].(float64) == 50 {
		t.Fatalf("customer site kept admin options: %v", s)
	}
	sites, _ := st.Sites(context.Background())
	if len(sites) != 2 {
		t.Fatalf("sites: %d", len(sites))
	}
	var list []map[string]any
	cust.list("/api/sites", &list)
	if len(list) != 1 || list[0]["name"] != "acmetest" {
		t.Fatalf("customer sees %v", list)
	}
	admin.list("/api/sites", &list)
	if len(list) != 2 {
		t.Fatalf("admin sees %d sites", len(list))
	}
	if code, _ := cust.do("PUT", "/api/sites/acmetest/nginx", map[string]string{"nginx_custom": "x"}, true); code != 403 {
		t.Errorf("customer custom nginx: %d", code)
	}
	if code, out := cust.do("GET", "/api/dashboard", nil, true); code != 200 || out["sites"].(float64) != 1 || out["customer"] != true {
		t.Errorf("customer dashboard: %d %v", code, out)
	}
	var jobs []map[string]any
	cust.list("/api/jobs", &jobs)
	for _, j := range jobs {
		if j["site"] != "acmetest" {
			t.Errorf("customer sees job %v", j)
		}
	}
	if code, _ := cust.do("GET", "/api/events?site=joshexamplecom", nil, true); code != 404 {
		t.Errorf("customer events of admin site: %d", code)
	}

	// Admins cannot delete an account that still has sites.
	if code, _ := admin.do("DELETE", "/api/accounts/"+acct, nil, true); code != 400 {
		t.Errorf("delete account with sites: %d", code)
	}
	// Suspension signs the customer out and blocks sign-in.
	if code, out := admin.do("PUT", "/api/accounts/"+acct, map[string]any{"name": "Acme Ltd", "suspended": true}, true); code != 200 {
		t.Fatalf("suspend: %d %v", code, out)
	}
	if code, _ := cust.do("GET", "/api/sites", nil, true); code != 401 {
		t.Errorf("suspended customer still signed in: %d", code)
	}
	c2 := &client{t: t, ts: ts}
	if code, _ := c2.do("POST", "/api/login", map[string]string{"username": "acme", "password": "acme acme acme 1"}, false); code != 403 {
		t.Errorf("suspended login: %d", code)
	}
}

func TestMigrateAccounts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.SaveUser(ctx, model.User{ID: "u1", Username: "josh", Email: "j@x.y"})
	_ = st.SaveSite(ctx, model.Site{ID: "a", Name: "a"})
	if err := st.MigrateAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.MigrateAccounts(ctx); err != nil { // twice is harmless
		t.Fatal(err)
	}
	accts, _ := st.Accounts(ctx)
	u, _ := st.UserByID(ctx, "u1")
	s, _ := st.Site(ctx, "a")
	if len(accts) != 1 || u.Role != model.RoleAdmin || u.AccountID != accts[0].ID || s.AccountID != accts[0].ID {
		t.Fatalf("accounts %v user %+v site %+v", accts, u, s)
	}
}
