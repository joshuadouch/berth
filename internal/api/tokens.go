package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// API tokens let other systems (a billing system such as WHMCS or Blesta,
// scripts) call Berth with "Authorization: Bearer <token>". A token acts as
// the admin who made it. A "provisioning" token may only use
// /api/provision/; a "full" token may use the whole API.

const (
	ScopeProvision = "provisioning"
	ScopeFull      = "full"
	tokensKey      = "api_tokens"
	tokenPrefix    = "berth_"
)

type apiToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	Hash      string    `json:"hash"` // sha256 of the secret part
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
}

// tokenView is what the API shows: never the hash.
type tokenView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	Prefix    string    `json:"prefix"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

var tokensMu sync.Mutex

func (s *Server) loadTokens(ctx context.Context) []apiToken {
	var out []apiToken
	_ = s.Store.GetKV(ctx, tokensKey, &out)
	return out
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

const tokenKey ctxKey = 2

// bearer returns the request's API token, if it carries one.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// tokenUser checks a token and returns the admin it acts as.
func (s *Server) tokenUser(ctx context.Context, raw string) (model.User, apiToken, error) {
	bad := errors.New("invalid API token")
	rest, ok := strings.CutPrefix(raw, tokenPrefix)
	if !ok {
		return model.User{}, apiToken{}, bad
	}
	id, secret, ok := strings.Cut(rest, ".")
	if !ok || id == "" || secret == "" {
		return model.User{}, apiToken{}, bad
	}
	tokensMu.Lock()
	defer tokensMu.Unlock()
	toks := s.loadTokens(ctx)
	for i, t := range toks {
		if t.ID != id {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(hashSecret(secret))) != 1 {
			return model.User{}, apiToken{}, bad
		}
		u, err := s.Store.UserByID(ctx, t.CreatedBy)
		if err != nil || !u.IsAdmin() {
			return model.User{}, apiToken{}, errors.New("the admin who made this API token is gone or no longer an admin")
		}
		if time.Since(t.LastUsed) > time.Minute {
			toks[i].LastUsed = time.Now().UTC()
			_ = s.Store.SetKV(context.WithoutCancel(ctx), tokensKey, toks)
		}
		return u, t, nil
	}
	return model.User{}, apiToken{}, bad
}

// tokenAllows reports whether a token may call a path.
func tokenAllows(t apiToken, path string) bool {
	return t.Scope == ScopeFull || strings.HasPrefix(path, "/api/provision/")
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	toks := s.loadTokens(r.Context())
	out := make([]tokenView, 0, len(toks))
	for _, t := range toks {
		v := tokenView{ID: t.ID, Name: t.Name, Scope: t.Scope, Prefix: tokenPrefix + t.ID + ".…", CreatedAt: t.CreatedAt, LastUsed: t.LastUsed}
		if u, err := s.Store.UserByID(r.Context(), t.CreatedBy); err == nil {
			v.CreatedBy = u.Username
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 60 {
		writeErr(w, 400, "give the token a name of up to 60 characters")
		return
	}
	if req.Scope == "" {
		req.Scope = ScopeProvision
	}
	if req.Scope != ScopeProvision && req.Scope != ScopeFull {
		writeErr(w, 400, "scope must be provisioning or full")
		return
	}
	if _, ok := s.impersonator(r); ok {
		writeErr(w, 403, "return to your own account first")
		return
	}
	u := currentUser(r)
	id := strings.ToLower(store.NewID())
	if len(id) > 12 {
		id = id[:12]
	}
	secret := auth.RandomToken(32)
	t := apiToken{ID: id, Name: req.Name, Scope: req.Scope, Hash: hashSecret(secret), CreatedBy: u.ID, CreatedAt: time.Now().UTC()}
	tokensMu.Lock()
	toks := append(s.loadTokens(r.Context()), t)
	err := s.Store.SetKV(r.Context(), tokensKey, toks)
	tokensMu.Unlock()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", "", u.Username+" created API token "+t.Name+" ("+t.Scope+")")
	writeJSON(w, 200, map[string]any{"id": t.ID, "name": t.Name, "scope": t.Scope, "token": tokenPrefix + t.ID + "." + secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tokensMu.Lock()
	toks := s.loadTokens(r.Context())
	out := make([]apiToken, 0, len(toks))
	name := ""
	for _, t := range toks {
		if t.ID == id {
			name = t.Name
			continue
		}
		out = append(out, t)
	}
	var err error
	if name != "" {
		err = s.Store.SetKV(r.Context(), tokensKey, out)
	}
	tokensMu.Unlock()
	if name == "" && err == nil {
		writeErr(w, 404, "no such token")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", "", currentUser(r).Username+" revoked API token "+name)
	writeJSON(w, 200, map[string]any{"ok": true})
}
