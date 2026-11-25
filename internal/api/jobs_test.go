package api

import (
	"fmt"
	"testing"
	"time"
)

// A customer who deletes a site can follow the delete job to the end, even
// though the job removes the site it belongs to.
func TestCustomerFollowsDeleteJob(t *testing.T) {
	deleteAsCustomer(t, false)
}

// deleteAsCustomer has customer "acme" create and delete a site, following the
// delete job, and returns acme and a second customer.
func deleteAsCustomer(t *testing.T, backup bool) (cust, other *client) {
	t.Helper()
	ts, _ := testServer(t)
	admin := &client{t: t, ts: ts}
	if code, out := admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	if code, out := admin.do("POST", "/api/accounts", map[string]any{"name": "Acme Ltd", "email": "ops@acme.test",
		"user": map[string]string{"username": "acme", "password": "acme acme acme 1"}}, true); code != 200 {
		t.Fatalf("create account: %d %v", code, out)
	}
	if code, out := admin.do("POST", "/api/accounts", map[string]any{"name": "Other", "email": "ops@other.test",
		"user": map[string]string{"username": "other", "password": "other other other 1"}}, true); code != 200 {
		t.Fatalf("create account: %d %v", code, out)
	}
	cust = &client{t: t, ts: ts}
	if code, out := cust.do("POST", "/api/login", map[string]string{"username": "acme", "password": "acme acme acme 1"}, false); code != 200 {
		t.Fatalf("login: %d %v", code, out)
	}
	other = &client{t: t, ts: ts}
	if code, out := other.do("POST", "/api/login", map[string]string{"username": "other", "password": "other other other 1"}, false); code != 200 {
		t.Fatalf("login: %d %v", code, out)
	}
	if code, out := cust.do("POST", "/api/sites", map[string]any{"type": "php", "domain": "acme.test", "php_version": "8.3", "enabled": true}, true); code != 200 {
		t.Fatalf("site: %d %v", code, out)
	}
	waitActive(t, cust, "acmetest")
	code, out := cust.do("DELETE", fmt.Sprintf("/api/sites/acmetest?backup=%d", map[bool]int{false: 0, true: 1}[backup]), nil, true)
	if code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	job := fmt.Sprint(int64(out["job_id"].(float64)))
	for i := 0; ; i++ {
		code, out = cust.do("GET", "/api/jobs/"+job, nil, true)
		if code != 200 {
			t.Fatalf("customer lost the delete job: %d %v", code, out)
		}
		if out["status"] != "running" {
			break
		}
		if i > 100 {
			t.Fatal("delete never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if out["status"] != "ok" {
		t.Fatalf("delete: %v", out)
	}
	if code, _ := cust.do("GET", "/api/sites/acmetest", nil, true); code != 404 {
		t.Fatalf("site still there: %d", code)
	}
	// Another account still cannot read it.
	if code, _ := other.do("GET", "/api/jobs/"+job, nil, true); code != 404 {
		t.Fatalf("other account reads the job: %d", code)
	}
	return cust, other
}

// A deleted site's final backup can still be listed, downloaded and deleted
// by its account, and only by its account.
func TestFinalBackupOutlivesSite(t *testing.T) {
	cust, other := deleteAsCustomer(t, true)
	code, out := cust.do("GET", "/api/backups", nil, true)
	if code != 200 {
		t.Fatalf("backups: %d %v", code, out)
	}
	list, _ := out["backups"].([]any)
	if len(list) != 1 {
		t.Fatalf("customer backups: %v", out)
	}
	b := list[0].(map[string]any)
	if b["site"] != "acmetest" || b["gone"] != true {
		t.Fatalf("backup: %v", b)
	}
	path := "/api/sites/acmetest/backups/" + b["file"].(string)
	if _, out := other.do("GET", "/api/backups", nil, true); len(out["backups"].([]any)) != 0 {
		t.Fatalf("other account sees %v", out)
	}
	for _, m := range []string{"GET", "DELETE"} {
		if code, _ := other.do(m, path, nil, true); code != 404 {
			t.Fatalf("%s as other account: %d", m, code)
		}
	}
	if code, _ := cust.do("GET", path, nil, true); code != 200 {
		t.Fatalf("download: %d", code)
	}
	if code, out := cust.do("DELETE", path, nil, true); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, out := cust.do("GET", "/api/backups", nil, true); len(out["backups"].([]any)) != 0 {
		t.Fatalf("still listed: %v", out)
	}
}
