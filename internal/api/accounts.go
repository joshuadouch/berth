package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// Access control ----------------------------------------------------------------

// adminOnly refuses users who are not admins.
func adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			writeErr(w, 403, "only an admin can do that")
			return
		}
		h(w, r)
	}
}

// ownSite refuses requests for a site the user's account does not own. A
// site of another account looks the same as one that does not exist.
func (s *Server) ownSite(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		site, err := s.Store.Site(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if !canSee(currentUser(r), site) {
			writeErr(w, 404, "not found")
			return
		}
		h(w, r)
	}
}

// canSee reports whether a user may open a site.
func canSee(u model.User, site model.Site) bool {
	return u.IsAdmin() || (u.AccountID != "" && site.AccountID == u.AccountID)
}

// visibleSites returns the sites the user may open. Admins may narrow the
// list to one account with ?account=.
func (s *Server) visibleSites(r *http.Request) ([]model.Site, error) {
	sites, err := s.Store.Sites(r.Context())
	if err != nil {
		return nil, err
	}
	u := currentUser(r)
	acct := ""
	if !u.IsAdmin() {
		acct = u.AccountID
	} else if q := r.URL.Query().Get("account"); q != "" {
		acct = q
	}
	if acct == "" {
		return sites, nil
	}
	out := sites[:0:0]
	for _, site := range sites {
		if site.AccountID == acct && canSee(u, site) {
			out = append(out, site)
		}
	}
	return out, nil
}

// siteNames returns the names of sites, for filtering logs and jobs.
func siteNames(sites []model.Site) []string {
	out := make([]string, len(sites))
	for i, site := range sites {
		out[i] = site.Name
	}
	return out
}

// mayOpenSiteName reports whether the user may see records of a site by name
// ("" is server-wide and for admins only).
func (s *Server) mayOpenSiteName(r *http.Request, name string) bool {
	u := currentUser(r)
	if u.IsAdmin() {
		return true
	}
	if name == "" {
		return false
	}
	site, err := s.Store.Site(r.Context(), name)
	return err == nil && canSee(u, site)
}

// Accounts ------------------------------------------------------------------------

type accountView struct {
	model.Account
	Users []model.User `json:"users"`
	Sites []string     `json:"sites"`
}

func (s *Server) accountView(ctx context.Context, a model.Account, sites []model.Site) (accountView, error) {
	users, err := s.Store.Users(ctx, a.ID)
	if err != nil {
		return accountView{}, err
	}
	v := accountView{Account: a, Users: users, Sites: []string{}}
	for _, site := range sites {
		if site.AccountID == a.ID {
			v.Sites = append(v.Sites, site.Name)
		}
	}
	return v, nil
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.Store.Accounts(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sites, _ := s.Store.Sites(r.Context())
	out := make([]accountView, 0, len(accounts))
	for _, a := range accounts {
		v, err := s.accountView(r.Context(), a, sites)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.Account(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	sites, _ := s.Store.Sites(r.Context())
	v, err := s.accountView(r.Context(), a, sites)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}

// userRequest is the form for a new or changed login.
type userRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// newUser checks a userRequest and builds the user.
func newUser(req userRequest, accountID string) (model.User, error) {
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 2 || len(req.Username) > 64 || strings.ContainsAny(req.Username, " \t/") {
		return model.User{}, errors.New("username must be 2-64 characters without spaces")
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		return model.User{}, errors.New("email looks invalid")
	}
	if req.Role == "" {
		req.Role = model.RoleCustomer
	}
	if req.Role != model.RoleAdmin && req.Role != model.RoleCustomer {
		return model.User{}, errors.New("role must be admin or customer")
	}
	h, err := auth.HashPassword(req.Password)
	if err != nil {
		return model.User{}, err
	}
	return model.User{ID: store.NewID(), Username: req.Username, Email: req.Email, PasswordHash: h, Role: req.Role, AccountID: accountID}, nil
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		model.Account
		User userRequest `json:"user"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a := req.Account
	a.ID, a.CreatedAt = store.NewID(), time.Now().UTC()
	if err := a.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.User.Email == "" {
		req.User.Email = a.Email
	}
	req.User.Role = model.RoleCustomer
	u, err := newUser(req.User, a.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := s.Store.UserByName(r.Context(), u.Username); err == nil {
		writeErr(w, 409, store.ErrNameTaken.Error())
		return
	}
	if err := s.Store.SaveAccount(r.Context(), a); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		_ = s.Store.DeleteAccount(r.Context(), a.ID)
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "account", "", currentUser(r).Username+" created account "+a.Name)
	v, _ := s.accountView(r.Context(), a, nil)
	writeJSON(w, 200, v)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	cur, err := s.Store.Account(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req model.Account
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Suspended && cur.ID == currentUser(r).AccountID {
		writeErr(w, 400, "you cannot suspend your own account")
		return
	}
	next := cur
	next.Name, next.Email, next.Notes, next.Suspended = req.Name, req.Email, req.Notes, req.Suspended
	if err := next.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.SaveAccount(r.Context(), next); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	msg := " updated account "
	switch {
	case next.Suspended && !cur.Suspended:
		msg = " suspended account "
		users, _ := s.Store.Users(r.Context(), next.ID)
		for _, u := range users {
			s.Store.DeleteUserSessions(r.Context(), u.ID)
		}
	case !next.Suspended && cur.Suspended:
		msg = " unsuspended account "
	}
	s.Store.AddEvent(r.Context(), "info", "account", "", currentUser(r).Username+msg+next.Name)
	writeJSON(w, 200, next)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.Account(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if a.ID == currentUser(r).AccountID {
		writeErr(w, 400, "you cannot delete your own account")
		return
	}
	sites, _ := s.Store.Sites(r.Context())
	for _, site := range sites {
		if site.AccountID == a.ID {
			writeErr(w, 400, "delete or move this account's sites first ("+site.Name+" belongs to it)")
			return
		}
	}
	users, _ := s.Store.Users(r.Context(), a.ID)
	for _, u := range users {
		if u.IsAdmin() {
			writeErr(w, 400, "this account has an admin ("+u.Username+"); remove them first")
			return
		}
	}
	if err := s.Store.DeleteAccount(r.Context(), a.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "account", "", currentUser(r).Username+" deleted account "+a.Name)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.Account(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req userRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := newUser(req, a.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := s.Store.UserByName(r.Context(), u.Username); err == nil {
		writeErr(w, 409, store.ErrNameTaken.Error())
		return
	}
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "account", "", currentUser(r).Username+" added "+u.Role+" "+u.Username+" to "+a.Name)
	writeJSON(w, 200, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		Role      string `json:"role"`
		ResetTOTP bool   `json:"reset_totp"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email != "" && !strings.Contains(req.Email, "@") {
		writeErr(w, 400, "email looks invalid")
		return
	}
	u.Email = req.Email
	if req.Role != "" && req.Role != u.Role {
		if req.Role != model.RoleAdmin && req.Role != model.RoleCustomer {
			writeErr(w, 400, "role must be admin or customer")
			return
		}
		if u.ID == currentUser(r).ID {
			writeErr(w, 400, "you cannot change your own role")
			return
		}
		u.Role = req.Role
	}
	logout := false
	if req.Password != "" {
		h, err := auth.HashPassword(req.Password)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		u.PasswordHash, logout = h, true
	}
	if req.ResetTOTP {
		u.TOTPEnabled, u.TOTPSecret, logout = false, "", true
	}
	if err := s.Store.SaveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if logout && u.ID != currentUser(r).ID {
		s.Store.DeleteUserSessions(r.Context(), u.ID)
	}
	s.Store.AddEvent(r.Context(), "info", "account", "", currentUser(r).Username+" updated user "+u.Username)
	writeJSON(w, 200, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if u.ID == currentUser(r).ID {
		writeErr(w, 400, "you cannot delete yourself")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), u.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "account", "", currentUser(r).Username+" removed user "+u.Username)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// moveSite gives a site to another account.
func (s *Server) moveSite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"account_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a, err := s.Store.Account(r.Context(), req.AccountID)
	if err != nil {
		writeErr(w, 400, "no such account")
		return
	}
	site, err := s.Engine.MoveSite(r.Context(), r.PathValue("id"), a.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.Store.AddEvent(r.Context(), "info", "site", site.Name, currentUser(r).Username+" moved "+site.Domain+" to account "+a.Name)
	writeJSON(w, 200, site)
}
