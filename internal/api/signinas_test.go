package api

import "testing"

func TestSignInAs(t *testing.T) {
	ts, _ := testServer(t)
	admin := &client{t: t, ts: ts}
	admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false)
	if code, out := admin.do("PUT", "/api/settings", map[string]any{"brand": map[string]any{"name": "HostCo", "ipv4": "203.0.113.10", "support_email": "help@hostco.test"},
		"backup_dir": "/tmp", "backup_keep": 7, "backup_time": "02:30", "default_php": "8.3"}, true); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	_, out := admin.do("POST", "/api/accounts", map[string]any{"name": "Acme", "user": map[string]string{"username": "acme", "password": "acme acme acme 1"}}, true)
	acct := out["id"].(string)
	_, me := admin.do("GET", "/api/me", nil, true)
	if code, _ := admin.do("POST", "/api/accounts/"+me["user"].(map[string]any)["account_id"].(string)+"/sign-in-as", nil, true); code != 400 {
		t.Errorf("signed in as an admin account: %d", code)
	}
	if code, out := admin.do("POST", "/api/accounts/"+acct+"/sign-in-as", nil, true); code != 200 {
		t.Fatalf("sign in as: %d %v", code, out)
	}
	_, me = admin.do("GET", "/api/me", nil, true)
	if me["user"].(map[string]any)["role"] != "customer" || me["impersonator"] != "josh" || me["brand"].(map[string]any)["name"] != "HostCo" {
		t.Fatalf("me while signed in as: %v", me)
	}
	if code, _ := admin.do("GET", "/api/plugins", nil, true); code != 403 {
		t.Errorf("customer view reached admin endpoints: %d", code)
	}
	if code, _ := admin.do("POST", "/api/me/password", map[string]string{"current": "x", "new": "yyyyyyyyyyyy"}, true); code != 403 {
		t.Errorf("changed the customer's password while signed in as them: %d", code)
	}
	if code, _ := admin.do("POST", "/api/sign-in-as/return", nil, true); code != 200 {
		t.Fatal("return")
	}
	_, me = admin.do("GET", "/api/me", nil, true)
	if me["user"].(map[string]any)["role"] != "admin" || me["impersonator"] != nil {
		t.Fatalf("after return: %v", me)
	}
	cust := &client{t: t, ts: ts}
	cust.do("POST", "/api/login", map[string]string{"username": "acme", "password": "acme acme acme 1"}, false)
	if code, _ := cust.do("POST", "/api/sign-in-as/return", nil, true); code != 400 {
		t.Errorf("customer returned to an admin: %d", code)
	}
	if code, _ := cust.do("POST", "/api/accounts/"+acct+"/sign-in-as", nil, true); code != 403 {
		t.Errorf("customer signed in as someone: %d", code)
	}
}
