package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u model.User) error {
	tok := auth.RandomToken(32)
	if err := s.Store.CreateSession(r.Context(), tok, u.ID, 7*24*time.Hour); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
	return nil
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUsers(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	team, aud, _ := s.accessSettings(r.Context())
	writeJSON(w, 200, map[string]any{
		"needs_setup": n == 0, "version": s.Version, "name": s.Cfg.Name,
		"access_enabled": team != "" && aud != "",
	})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUsers(r.Context())
	if err != nil || n > 0 {
		writeErr(w, 403, "setup is already complete")
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 2 || len(req.Username) > 64 {
		writeErr(w, 400, "username must be 2-64 characters")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		writeErr(w, 400, "email looks invalid")
		return
	}
	h, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	acct := model.Account{ID: store.NewID(), Name: req.Username, Email: req.Email, CreatedAt: time.Now().UTC()}
	if err := s.Store.SaveAccount(r.Context(), acct); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	u := model.User{ID: store.NewID(), Username: req.Username, Email: req.Email, PasswordHash: h, Role: model.RoleAdmin, AccountID: acct.ID}
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Use the admin's email for new WordPress sites unless set otherwise.
	if st, err := s.Store.Settings(r.Context()); err == nil && st.WPAdminEmail == "" && u.Email != "" {
		st.WPAdminEmail = u.Email
		_ = s.Store.SetSettings(r.Context(), st)
	}
	s.Store.AddEvent(r.Context(), "info", "auth", "", "Admin "+u.Username+" created")
	_ = s.setSession(w, r, u)
	writeJSON(w, 200, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		writeErr(w, 429, "too many failed attempts; try again in 15 minutes")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.Store.UserByName(r.Context(), strings.TrimSpace(req.Username))
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		s.limiter.fail(ip)
		writeErr(w, 401, "wrong username or password")
		return
	}
	if !u.IsAdmin() {
		if a, err := s.Store.Account(r.Context(), u.AccountID); err != nil || a.Suspended {
			writeErr(w, 403, "this account is suspended; contact your host")
			return
		}
	}
	if u.TOTPEnabled {
		if req.TOTP == "" {
			writeJSON(w, 401, map[string]any{"error": "enter your authenticator code", "totp_required": true})
			return
		}
		if !auth.CheckTOTP(u.TOTPSecret, req.TOTP) {
			s.limiter.fail(ip)
			writeJSON(w, 401, map[string]any{"error": "wrong authenticator code", "totp_required": true})
			return
		}
	}
	if err := s.setSession(w, r, u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	a, _ := s.Store.Account(r.Context(), u.AccountID)
	writeJSON(w, 200, map[string]any{"user": u, "account": a, "name": s.Cfg.Name, "version": s.Version, "plugins": s.myPlugins(r), "usage": s.planUsage(r.Context(), u.AccountID)})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	if !auth.CheckPassword(u.PasswordHash, req.Current) {
		writeErr(w, 400, "current password is wrong")
		return
	}
	h, err := auth.HashPassword(req.New)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u.PasswordHash = h
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.TOTPEnabled {
		writeErr(w, 400, "two-factor is already on")
		return
	}
	u.TOTPSecret = auth.NewTOTPSecret()
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"secret": u.TOTPSecret, "uri": auth.TOTPURI(u.TOTPSecret, u.Username)})
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	if u.TOTPSecret == "" || !auth.CheckTOTP(u.TOTPSecret, req.Code) {
		writeErr(w, 400, "that code is not valid")
		return
	}
	u.TOTPEnabled = true
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	if !u.TOTPEnabled || !auth.CheckTOTP(u.TOTPSecret, req.Code) {
		writeErr(w, 400, "that code is not valid")
		return
	}
	u.TOTPEnabled, u.TOTPSecret = false, ""
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
