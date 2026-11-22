package api

import (
	"context"
	"net/http"
	"time"

	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/model"
)

// Admins can sign in as a customer to see what they see. The customer's
// session remembers the admin, who returns with one click.

type impersonation struct {
	Admin string    `json:"admin"` // the admin's user ID
	Since time.Time `json:"since"`
}

const impersonateTTL = 2 * time.Hour

func impersonationKey(token string) string { return "impersonate:" + token }

// impersonator returns the admin behind the request's session, if any.
func (s *Server) impersonator(r *http.Request) (model.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return model.User{}, false
	}
	var imp impersonation
	if err := s.Store.GetKV(r.Context(), impersonationKey(c.Value), &imp); err != nil || imp.Admin == "" {
		return model.User{}, false
	}
	u, err := s.Store.UserByID(r.Context(), imp.Admin)
	return u, err == nil
}

func (s *Server) signInAs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	admin := currentUser(r)
	a, err := s.Store.Account(ctx, r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if a.Suspended {
		writeErr(w, 400, "the account is suspended; unsuspend it first")
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	_ = readJSON(r, &req)
	users, err := s.Store.Users(ctx, a.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var target *model.User
	for i, u := range users {
		if u.IsAdmin() || req.UserID != "" && u.ID != req.UserID {
			continue
		}
		target = &users[i]
		break
	}
	if target == nil {
		writeErr(w, 400, "the account has no customer login to sign in as")
		return
	}
	tok := auth.RandomToken(32)
	if err := s.Store.CreateSession(ctx, tok, target.ID, impersonateTTL); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.Store.SetKV(ctx, impersonationKey(tok), impersonation{Admin: admin.ID, Since: time.Now().UTC()}); err != nil {
		s.Store.DeleteSession(ctx, tok)
		writeErr(w, 500, err.Error())
		return
	}
	// The admin's own session ends here; they come back through /return.
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(ctx, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(impersonateTTL.Seconds())})
	s.Store.AddEvent(ctx, "info", "account", "", admin.Username+" signed in as "+target.Username+" ("+a.Name+")")
	writeJSON(w, 200, map[string]any{"ok": true, "username": target.Username})
}

func (s *Server) signInAsReturn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	admin, ok := s.impersonator(r)
	if !ok {
		writeErr(w, 400, "you are not signed in as someone else")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(ctx, c.Value)
		_ = s.Store.DeleteKV(context.WithoutCancel(ctx), impersonationKey(c.Value))
	}
	if err := s.setSession(w, r, admin); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
