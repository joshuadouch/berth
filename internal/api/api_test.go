package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins"
	"github.com/joshuadouch/berth/internal/store"
)

func testServer(t *testing.T) (*httptest.Server, *store.Store) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir, cfg.SitesDir, cfg.LogDir = dir, filepath.Join(dir, "sites"), filepath.Join(dir, "log")
	cfg.Nginx.ConfDir = filepath.Join(dir, "nginx")
	cfg.PHP.PoolDir = filepath.Join(dir, "php", "{v}")
	cfg.DryRun = true
	st, err := store.Open(filepath.Join(dir, "berth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	settings := model.Settings{BackupDir: filepath.Join(dir, "backups"), WPAdminEmail: "a@b.c"}
	_ = settings.Normalize()
	_ = st.SetSettings(context.Background(), settings)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eng := engine.New(cfg, st, log)
	srv := &Server{Cfg: cfg, Store: st, Engine: eng, Log: log, Version: "test", Plugins: plugin.NewRegistry(st, log, plugins.All()...)}
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

type client struct {
	t      *testing.T
	ts     *httptest.Server
	cookie *http.Cookie
}

func (c *client) do(method, path string, body any, header bool) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.ts.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if header {
		req.Header.Set("X-Berth", "1")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	resp, err := c.ts.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			c.cookie = ck
		}
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSetupLoginAndSites(t *testing.T) {
	ts, _ := testServer(t)
	c := &client{t: t, ts: ts}
	if code, _ := c.do("GET", "/api/sites", nil, true); code != 401 {
		t.Fatalf("sites without login: %d", code)
	}
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "josh", "password": "short"}, false); code != 400 {
		t.Fatalf("weak password accepted: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "josh", "email": "josh@example.com", "password": "correct horse battery"}, false); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/setup", map[string]string{"username": "eve", "password": "correct horse battery"}, false); code != 403 {
		t.Fatalf("second setup allowed: %d", code)
	}
	site := map[string]any{"type": "php_db", "domain": "app.example.com", "php_version": "8.3", "enabled": true, "https": true}
	if code, _ := c.do("POST", "/api/sites", site, false); code != 403 {
		t.Fatalf("missing X-Berth header allowed: %d", code)
	}
	code, out := c.do("POST", "/api/sites", site, true)
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	s := out["site"].(map[string]any)
	if s["name"] != "appexamplecom" || s["db"].(map[string]any)["name"] != "site_appexamplecom" {
		t.Fatalf("site %v", s)
	}
	if code, out := c.do("POST", "/api/sites", site, true); code != 400 {
		t.Fatalf("duplicate site allowed: %d %v", code, out)
	}
	// The other login path.
	c2 := &client{t: t, ts: ts}
	if code, _ := c2.do("POST", "/api/login", map[string]string{"username": "josh", "password": "nope nope nope"}, false); code != 401 {
		t.Fatal("wrong password accepted")
	}
	if code, _ := c2.do("POST", "/api/login", map[string]string{"username": "josh", "password": "correct horse battery"}, false); code != 200 {
		t.Fatal("login failed")
	}
	if code, out := c2.do("GET", "/api/me", nil, false); code != 200 || out["user"].(map[string]any)["username"] != "josh" {
		t.Fatalf("me: %d %v", code, out)
	}
}
