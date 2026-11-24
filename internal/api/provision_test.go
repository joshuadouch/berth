package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// bearerDo calls the API with an API token.
func bearerDo(t *testing.T, c *client, token, method, path string, body any) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.ts.URL+path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestProvisioningAPI(t *testing.T) {
	ts, _ := testServer(t)
	admin := &client{t: t, ts: ts}
	admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false)
	_, plan := admin.do("POST", "/api/plans", map[string]any{"name": "Starter", "max_sites": 2}, true)
	_, pro := admin.do("POST", "/api/plans", map[string]any{"name": "Pro", "max_sites": 10}, true)

	code, out := admin.do("POST", "/api/tokens", map[string]any{"name": "WHMCS"}, true)
	if code != 200 || out["scope"] != ScopeProvision {
		t.Fatalf("token: %d %v", code, out)
	}
	tok := out["token"].(string)
	_, full := admin.do("POST", "/api/tokens", map[string]any{"name": "script", "scope": "full"}, true)

	// Scope and bad tokens.
	if code, _ := bearerDo(t, admin, tok, "GET", "/api/accounts", nil); code != 403 {
		t.Errorf("provisioning token reached the admin API: %d", code)
	}
	if code, _ := bearerDo(t, admin, full["token"].(string), "GET", "/api/plugins", nil); code != 200 {
		t.Errorf("full token refused: %d", code)
	}
	if code, _ := bearerDo(t, admin, tok+"x", "GET", "/api/provision/plans", nil); code != 401 {
		t.Errorf("wrong secret accepted: %d", code)
	}

	// A billing system keeps plans in step: create by name, then update in place.
	code, out = bearerDo(t, admin, tok, "PUT", "/api/provision/plans/Plus", map[string]any{"max_sites": 5, "disk_mb": 30720, "cpu_percent": 100, "ram_mb": 2048, "max_processes": 100})
	if code != 201 || out["name"] != "Plus" || out["max_sites"] != float64(5) {
		t.Fatalf("create plan: %d %v", code, out)
	}
	plusID := out["id"]
	code, out = bearerDo(t, admin, tok, "PUT", "/api/provision/plans/plus", map[string]any{"name": "Plus", "max_sites": 6, "allow_shell": true})
	if code != 200 || out["id"] != plusID || out["max_sites"] != float64(6) || out["disk_mb"] != float64(0) || out["allow_shell"] != true {
		t.Fatalf("update plan: %d %v", code, out)
	}
	if code, _ := bearerDo(t, admin, tok, "PUT", "/api/provision/plans/Plus", map[string]any{"name": "Pro"}); code != 409 {
		t.Errorf("rename onto another plan's name: %d", code)
	}
	if code, _ := bearerDo(t, admin, tok, "PUT", "/api/provision/plans/Bad", map[string]any{"memory_mb": 8}); code != 400 {
		t.Errorf("invalid plan accepted: %d", code)
	}

	// Create on a plan, by name.
	code, out = bearerDo(t, admin, tok, "POST", "/api/provision/accounts", map[string]any{"name": "Acme Ltd", "email": "Owner@Acme.test", "plan": "starter", "external_id": "svc-42"})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	// Plugins a billing system can set up for an account.
	if code, _ := bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/plugins/cloudflare", map[string]any{}); code != 400 {
		t.Errorf("non-provisioning plugin: %d", code)
	}
	if code, _ := bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/plugins/mailcow", map[string]any{"domain": "acme.test"}); code != 409 {
		t.Errorf("turned-off plugin: %d", code)
	}
	acct := out["account"].(map[string]any)
	if acct["plan_id"] != plan["id"] || acct["external_id"] != "svc-42" || out["username"] != "owner" || len(out["password"].(string)) != 20 {
		t.Fatalf("created: %v", out)
	}
	password := out["password"].(string)
	if code, out := bearerDo(t, admin, tok, "POST", "/api/provision/accounts", map[string]any{"name": "Again", "email": "x@acme.test", "external_id": "svc-42"}); code != 409 || out["account_id"] != acct["id"] {
		t.Errorf("duplicate external ID: %d %v", code, out)
	}
	if code, _ := bearerDo(t, admin, tok, "POST", "/api/provision/accounts", map[string]any{"email": "y@acme.test", "plan": "Gold"}); code != 400 {
		t.Errorf("unknown plan accepted: %d", code)
	}
	cust := &client{t: t, ts: ts}
	if code, _ := cust.do("POST", "/api/login", map[string]string{"username": "owner", "password": password}, false); code != 200 {
		t.Fatal("customer cannot sign in with the generated password")
	}

	// Look up by external ID, change plan.
	if code, out := bearerDo(t, admin, tok, "GET", "/api/provision/accounts/ext:svc-42", nil); code != 200 || out["id"] != acct["id"] {
		t.Fatalf("get by external ID: %d %v", code, out)
	}
	if code, out := bearerDo(t, admin, tok, "PATCH", "/api/provision/accounts/ext:svc-42", map[string]any{"plan_id": pro["id"]}); code != 200 || out["plan_id"] != pro["id"] || out["name"] != "Acme Ltd" {
		t.Fatalf("change plan: %d %v", code, out)
	}

	// Suspend locks the customer out; unsuspend lets them back.
	if code, _ := bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/suspend", map[string]any{"reason": "unpaid"}); code != 200 {
		t.Fatal("suspend")
	}
	if code, _ := cust.do("GET", "/api/me", nil, false); code != 401 {
		t.Errorf("suspended customer still signed in: %d", code)
	}
	if code, _ := bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/login-link", nil); code != 400 {
		t.Errorf("login link for a suspended account: %d", code)
	}
	if code, _ := bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/unsuspend", nil); code != 200 {
		t.Fatal("unsuspend")
	}

	// Password reset and one-time link.
	code, out = bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/password", nil)
	if code != 200 || out["password"] == nil {
		t.Fatalf("password: %d %v", code, out)
	}
	code, out = bearerDo(t, admin, tok, "POST", "/api/provision/accounts/ext:svc-42/login-link", nil)
	if code != 200 || !strings.HasPrefix(out["url"].(string), ts.URL+"/api/sso/") {
		t.Fatalf("login link: %d %v", code, out)
	}
	link := out["path"].(string)
	sso := &client{t: t, ts: ts}
	noRedirect := *ts.Client()
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Get(ts.URL + link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("sso: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			sso.cookie = ck
		}
	}
	if code, me := sso.do("GET", "/api/me", nil, false); code != 200 || me["user"].(map[string]any)["username"] != "owner" {
		t.Fatalf("sso session: %d %v", code, me)
	}
	resp, _ = noRedirect.Get(ts.URL + link)
	resp.Body.Close()
	if resp.Header.Get("Location") == "/" {
		t.Error("one-time link worked twice")
	}

	// Terminate: refused while sites exist, unless asked to delete them.
	if code, out := admin.do("POST", "/api/sites", map[string]any{"type": "php", "domain": "acme.test", "php_version": "8.3", "enabled": true, "account_id": acct["id"]}, true); code != 200 {
		t.Fatalf("site: %d %v", code, out)
	}
	if code, out := bearerDo(t, admin, tok, "DELETE", "/api/provision/accounts/ext:svc-42", nil); code != 409 {
		t.Fatalf("terminate with sites: %d %v", code, out)
	}
	if code, out := bearerDo(t, admin, tok, "DELETE", "/api/provision/accounts/ext:svc-42?delete_sites=1", nil); code != 202 {
		t.Fatalf("terminate: %d %v", code, out)
	}
	for i := 0; i < 300; i++ {
		if code, _ := bearerDo(t, admin, tok, "GET", "/api/provision/accounts/ext:svc-42", nil); code == 404 {
			break
		}
		if i == 299 {
			var ev, jobs, sites []map[string]any
			admin.list("/api/events", &ev)
			admin.list("/api/jobs", &jobs)
			admin.list("/api/sites", &sites)
			t.Fatalf("account was not deleted after its sites:\n%v\n%v\n%v", ev, jobs, sites)
		}
		waitABit()
	}

	// Revoking a token stops it.
	if code, _ := admin.do("DELETE", "/api/tokens/"+out2id(tok), nil, true); code != 200 {
		t.Fatal("revoke")
	}
	if code, _ := bearerDo(t, admin, tok, "GET", "/api/provision/plans", nil); code != 401 {
		t.Errorf("revoked token still works: %d", code)
	}
}

func out2id(tok string) string {
	id, _, _ := strings.Cut(strings.TrimPrefix(tok, tokenPrefix), ".")
	return id
}

func TestBrandAssets(t *testing.T) {
	ts, _ := testServer(t)
	admin := &client{t: t, ts: ts}
	admin.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false)
	put := func(kind string, body []byte) int {
		req, _ := http.NewRequest("PUT", ts.URL+"/api/settings/brand/"+kind, bytes.NewReader(body))
		req.Header.Set("X-Berth", "1")
		req.AddCookie(admin.cookie)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	png := []byte("\x89PNG\r\n\x1a\n0000")
	if code := put("logo", []byte("<html><script>alert(1)</script></html>")); code != 400 {
		t.Errorf("html accepted as a logo: %d", code)
	}
	if code := put("favicon", []byte("\xff\xd8\xff0000")); code != 400 {
		t.Errorf("jpeg accepted as a favicon: %d", code)
	}
	if code := put("logo", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)); code != 200 {
		t.Fatalf("svg logo: %d", code)
	}
	if code := put("favicon", png); code != 200 {
		t.Fatalf("png favicon: %d", code)
	}
	resp, err := ts.Client().Get(ts.URL + "/api/brand/logo")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") || !bytes.Contains(b, []byte("<svg")) {
		t.Fatalf("logo served: %d %v", resp.StatusCode, resp.Header)
	}
	_, setup := admin.do("GET", "/api/setup", nil, false)
	if !strings.HasPrefix(setup["logo"].(string), "logo-") || !strings.HasSuffix(setup["favicon"].(string), ".png") {
		t.Fatalf("setup: %v", setup)
	}
	// Saving settings keeps the images.
	if code, out := admin.do("PUT", "/api/settings", map[string]any{"brand": map[string]any{"name": "HostCo", "logo": "x", "favicon": ""},
		"backup_dir": "/tmp", "backup_keep": 7, "backup_time": "02:30", "default_php": "8.3"}, true); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	_, setup2 := admin.do("GET", "/api/setup", nil, false)
	if setup2["logo"] != setup["logo"] {
		t.Errorf("saving settings dropped the logo: %v", setup2)
	}
	if code, _ := admin.do("DELETE", "/api/settings/brand/logo", nil, true); code != 200 {
		t.Fatal("delete")
	}
	resp, _ = ts.Client().Get(ts.URL + "/api/brand/logo")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("deleted logo still served: %d", resp.StatusCode)
	}
}

func waitABit() { time.Sleep(50 * time.Millisecond) }
