// Package api serves the Berth REST API and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/store"
)

// Server is the HTTP API.
type Server struct {
	Cfg     *config.Config
	Store   *store.Store
	Engine  *engine.Engine
	Log     *slog.Logger
	UI      fs.FS
	Access  *auth.AccessVerifier
	Version string
	Plugins *plugin.Registry

	limiter *loginLimiter
}

type ctxKey int

const userKey ctxKey = 1

const sessionCookie = "berth_session"

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	if s.Access == nil {
		s.Access = &auth.AccessVerifier{}
	}
	if s.Plugins == nil {
		s.Plugins = plugin.NewRegistry(s.Store, s.Log)
	}
	s.limiter = &loginLimiter{fails: map[string][]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": s.Version, "name": s.Cfg.Name})
	})
	mux.HandleFunc("GET /api/setup", s.getSetup)
	mux.HandleFunc("POST /api/setup", s.postSetup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)

	// a: any signed-in user. as: a site the user's account owns (admins may
	// open every site). ad: admins only.
	a := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(h)) }
	as := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(s.ownSite(h))) }
	ad := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(adminOnly(h))) }
	a("GET /api/me", s.me)
	a("POST /api/me/password", s.changePassword)
	a("POST /api/me/totp", s.totpSetup)
	a("POST /api/me/totp/enable", s.totpEnable)
	a("POST /api/me/totp/disable", s.totpDisable)

	a("GET /api/dashboard", s.dashboard)
	a("GET /api/events", s.events)
	a("GET /api/jobs", s.listJobs)
	a("GET /api/jobs/{id}", s.getJob)

	a("GET /api/sites", s.listSites)
	a("POST /api/sites", s.createSite)
	a("POST /api/imports", s.uploadImport)
	a("DELETE /api/imports/{id}", s.deleteImport)
	as("GET /api/sites/{id}", s.getSite)
	as("PUT /api/sites/{id}", s.updateSite)
	as("DELETE /api/sites/{id}", s.deleteSite)
	as("POST /api/sites/{id}/retry", s.retrySite)
	as("POST /api/sites/{id}/tls", s.requestCert)
	as("POST /api/sites/{id}/fix-permissions", s.fixPermissions)
	as("GET /api/sites/{id}/nginx", s.siteNginx)
	ad("PUT /api/sites/{id}/nginx", s.setSiteNginx)
	as("GET /api/sites/{id}/logs", s.siteLogs)
	as("POST /api/sites/{id}/db/password", s.rotateDBPassword)
	as("POST /api/sites/{id}/sftp/password", s.sftpPassword)
	as("GET /api/sites/{id}/wp", s.wpInfo)
	as("POST /api/sites/{id}/wp/update", s.wpUpdate)
	as("POST /api/sites/{id}/wp/admin-password", s.wpAdminPassword)
	as("POST /api/sites/{id}/wp/login", s.wpLogin)
	as("POST /api/sites/{id}/wp/search-replace", s.wpSearchReplace)
	as("PUT /api/sites/{id}/auto-update", s.setAutoUpdate)
	as("GET /api/sites/{id}/vulns", s.siteVulns)
	as("POST /api/sites/{id}/vulns/scan", s.scanVulns)
	as("GET /api/sites/{id}/uptime", s.siteUptime)
	as("GET /api/sites/{id}/traffic", s.siteTraffic)
	as("GET /api/sites/{id}/performance", s.sitePerformance)
	as("GET /api/sites/{id}/resources", s.siteResources)
	ad("GET /api/resources", s.serverResources)
	ad("POST /api/sites/{id}/halyard", s.halyardSync)
	ad("DELETE /api/sites/{id}/halyard", s.halyardUnlink)
	ad("PUT /api/sites/{id}/account", s.moveSite)

	a("GET /api/backups", s.listBackups)
	ad("POST /api/backups/run", s.runNightly)
	as("POST /api/sites/{id}/backups", s.createBackup)
	as("GET /api/sites/{id}/backups/{file}", s.downloadBackup)
	as("POST /api/sites/{id}/backups/{file}/restore", s.restoreBackup)
	as("DELETE /api/sites/{id}/backups/{file}", s.deleteBackup)
	as("GET /api/sites/{id}/backups/{file}/browse", s.browseBackup)
	as("GET /api/sites/{id}/backups/{file}/file", s.backupFile)

	as("GET /api/sites/{id}/files", s.listFiles)
	as("GET /api/sites/{id}/files/content", s.readFile)
	as("GET /api/sites/{id}/files/download", s.downloadFile)
	as("POST /api/sites/{id}/files", s.fileAction)
	as("POST /api/sites/{id}/files/upload", s.uploadFiles)

	as("GET /api/sites/{id}/db/tables", s.dbTables)
	as("GET /api/sites/{id}/db/tables/{table}", s.dbBrowse)
	as("POST /api/sites/{id}/db/tables/{table}/insert", s.dbChange("insert"))
	as("POST /api/sites/{id}/db/tables/{table}/update", s.dbChange("update"))
	as("POST /api/sites/{id}/db/tables/{table}/delete", s.dbChange("delete"))
	as("POST /api/sites/{id}/db/sql", s.dbSQL)
	ad("POST /api/settings/s3/test", s.testS3)

	a("GET /api/settings", s.getSettings)
	ad("PUT /api/settings", s.putSettings)
	ad("GET /api/halyard", s.halyardInfo)
	ad("POST /api/halyard/pool", s.halyardPool)
	ad("GET /api/system", s.system)
	ad("POST /api/system/php/{v}", s.installPHP)
	ad("POST /api/system/php-updates/check", s.checkPHPUpdates)
	ad("POST /api/system/php-updates/apply", s.applyPHPUpdates)
	ad("POST /api/system/apply", s.reapply)
	ad("POST /api/update", s.startUpdate)
	ad("POST /api/update/check", s.checkUpdate)

	ad("GET /api/plans", s.listPlans)
	ad("POST /api/plans", s.savePlan)
	ad("PUT /api/plans/{id}", s.savePlan)
	ad("DELETE /api/plans/{id}", s.deletePlan)
	ad("GET /api/accounts", s.listAccounts)
	ad("POST /api/accounts", s.createAccount)
	ad("GET /api/accounts/{id}", s.getAccount)
	ad("POST /api/accounts/{id}/sign-in-as", s.signInAs)
	a("POST /api/sign-in-as/return", s.signInAsReturn)
	ad("PUT /api/accounts/{id}", s.updateAccount)
	ad("DELETE /api/accounts/{id}", s.deleteAccount)
	ad("POST /api/accounts/{id}/users", s.createUser)
	ad("PUT /api/users/{id}", s.updateUser)
	ad("DELETE /api/users/{id}", s.deleteUser)

	ad("GET /api/plugins", s.listPlugins)
	ad("PUT /api/plugins/{id}", s.savePlugin)
	ad("POST /api/plugins/{id}/test", s.testPlugin)
	ad("PUT /api/plugins/{id}/accounts", s.pluginAccounts)
	mux.Handle("/api/p/{id}/", s.requireAuth(s.pluginGate(s.Plugins.ServeHTTP)))

	ad("GET /api/tokens", s.listTokens)
	ad("POST /api/tokens", s.createToken)
	ad("DELETE /api/tokens/{id}", s.deleteToken)

	// Provisioning API for billing systems (see provision.go).
	ad("GET /api/provision/plans", s.provPlans)
	ad("PUT /api/provision/plans/{ref}", s.provPutPlan)
	ad("GET /api/provision/accounts", s.provListAccounts)
	ad("POST /api/provision/accounts", s.provCreateAccount)
	ad("GET /api/provision/accounts/{id}", s.provGetAccount)
	ad("PATCH /api/provision/accounts/{id}", s.provUpdateAccount)
	ad("POST /api/provision/accounts/{id}/suspend", s.provSuspend(true))
	ad("POST /api/provision/accounts/{id}/unsuspend", s.provSuspend(false))
	ad("POST /api/provision/accounts/{id}/password", s.provPassword)
	ad("POST /api/provision/accounts/{id}/login-link", s.provLoginLink)
	ad("DELETE /api/provision/accounts/{id}", s.provTerminate)
	ad("POST /api/provision/accounts/{id}/plugins/{plugin}", s.provPlugin)
	mux.HandleFunc("GET /api/sso/{token}", s.sso)

	mux.HandleFunc("GET /api/brand/{kind}", s.brandAsset)
	ad("PUT /api/settings/brand/{kind}", s.putBrandAsset)
	ad("DELETE /api/settings/brand/{kind}", s.deleteBrandAsset)

	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 404, "no such endpoint")
	}))
	if s.UI != nil {
		mux.Handle("/", spaHandler(s.UI))
	}
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

func spaHandler(ui fs.FS) http.Handler {
	files := http.FileServer(http.FS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := ui.Open(p); err == nil {
				st, _ := f.Stat()
				f.Close()
				if st != nil && !st.IsDir() {
					if strings.HasPrefix(p, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		b, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "UI not built", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}

// requireAuth accepts an API token, a session cookie or a Cloudflare Access JWT. Mutating
// requests also need the X-Berth header, which browsers will not send
// cross-site without a CORS preflight we never allow.
func (s *Server) requireAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw, ok := bearer(r); ok {
			// API tokens: no cookie is involved, so no X-Berth header is needed.
			ip := clientIP(r)
			if s.limiter.blocked(ip) {
				writeErr(w, 429, "too many failed attempts; try again in 15 minutes")
				return
			}
			u, t, err := s.tokenUser(r.Context(), raw)
			if err != nil {
				s.limiter.fail(ip)
				writeErr(w, 401, err.Error())
				return
			}
			if !tokenAllows(t, r.URL.Path) {
				writeErr(w, 403, "this API token may only use /api/provision/")
				return
			}
			ctx := context.WithValue(r.Context(), userKey, u)
			h(w, r.WithContext(context.WithValue(ctx, tokenKey, t)))
			return
		}
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, 401, "sign in required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Berth") != "1" {
			writeErr(w, 403, "missing X-Berth header")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (s *Server) authenticate(r *http.Request) (model.User, error) {
	u, err := s.credentials(r)
	if err != nil {
		return u, err
	}
	if !u.IsAdmin() {
		if a, err := s.Store.Account(r.Context(), u.AccountID); err != nil || a.Suspended {
			return model.User{}, errors.New("account suspended")
		}
	}
	return u, nil
}

func (s *Server) credentials(r *http.Request) (model.User, error) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			return u, nil
		}
	}
	tok := r.Header.Get("Cf-Access-Jwt-Assertion")
	if tok == "" {
		if c, err := r.Cookie("CF_Authorization"); err == nil {
			tok = c.Value
		}
	}
	if tok == "" {
		return model.User{}, errors.New("no credentials")
	}
	team, aud, emails := s.accessSettings(r.Context())
	if team == "" || aud == "" {
		return model.User{}, errors.New("access not configured")
	}
	email, err := s.Access.Verify(r.Context(), team, aud, tok)
	if err != nil {
		return model.User{}, err
	}
	admin, err := s.Store.FirstUser(r.Context())
	if err != nil {
		return model.User{}, err
	}
	if strings.EqualFold(admin.Email, email) {
		return admin, nil
	}
	for _, allowed := range emails {
		if strings.EqualFold(allowed, email) {
			return admin, nil
		}
	}
	return model.User{}, errors.New("access email is not allowed")
}

func currentUser(r *http.Request) model.User {
	u, _ := r.Context().Value(userKey).(model.User)
	return u
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not found")
		return
	}
	writeErr(w, 500, err.Error())
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

// loginLimiter slows down password guessing per client IP.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	var keep []time.Time
	for _, t := range l.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.fails[ip] = keep
	return len(keep) >= 10
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.fails[ip], time.Now())
}
