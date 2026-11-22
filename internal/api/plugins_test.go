package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPluginsAPI(t *testing.T) {
	ts, _ := testServer(t)
	c := &client{t: t, ts: ts}
	if code, _ := c.do("GET", "/api/p/mailcow/overview", nil, true); code != 401 {
		t.Fatalf("plugin API without login: %d", code)
	}
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/p/mailcow/overview", nil, true); code != 409 {
		t.Fatalf("plugin off should answer 409, got %d", code)
	}
	if code, _ := c.do("GET", "/api/p/nope/x", nil, true); code != 404 {
		t.Fatalf("unknown plugin: %d", code)
	}
	if code, out := c.do("PUT", "/api/plugins/mailcow", map[string]any{"enabled": true, "config": map[string]any{"url": "https://mail.example.com"}}, true); code != 400 {
		t.Fatalf("enabling without API key: %d %v", code, out)
	}
	cfg := map[string]any{"url": "https://mail.example.com", "api_key": "abc", "ses_access_key": "AKID", "ses_secret_key": "shh"}
	if code, _ := c.do("PUT", "/api/plugins/mailcow", map[string]any{"enabled": true, "config": cfg}, false); code != 403 {
		t.Fatal("missing X-Berth header allowed")
	}
	code, out := c.do("PUT", "/api/plugins/mailcow", map[string]any{"enabled": true, "config": cfg}, true)
	if code != 200 || out["enabled"] != true {
		t.Fatalf("enable: %d %v", code, out)
	}
	conf := out["config"].(map[string]any)
	if _, leaked := conf["api_key"]; leaked || conf["ses_default"] != true || out["secrets_set"].(map[string]any)["ses_secret_key"] != true {
		t.Fatalf("view: %v", out)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/plugins", nil)
	req.AddCookie(c.cookie)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list) != 3 || list[2]["id"] != "mailcow" || list[2]["nav_label"] != "Mail" {
		t.Fatalf("list: %v %v", list, err)
	}
	// Enabled: requests now reach the plugin (mailcow itself is unreachable here,
	// so the overview reports the error rather than failing).
	if code, _ := c.do("GET", "/api/p/mailcow/nope", nil, true); code != 404 {
		t.Fatalf("unknown plugin endpoint: %d", code)
	}
}
