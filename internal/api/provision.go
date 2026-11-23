package api

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// The provisioning API is for billing systems: create an account on a plan,
// change its plan, suspend, unsuspend, terminate, reset the password and
// hand out a one-time sign-in link. Every route takes an account's Berth ID
// or "ext:<external ID>" (the billing system's own ID).

// actor names who made a change, for the activity log.
func actor(r *http.Request) string {
	name := currentUser(r).Username
	if t, ok := r.Context().Value(tokenKey).(apiToken); ok {
		name += " (API token " + t.Name + ")"
	}
	return name
}

// readOptionalJSON reads a JSON body when there is one.
func readOptionalJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return readJSON(r, v)
}

// findPlan looks a plan up by ID or by name.
func (s *Server) findPlan(ctx context.Context, ref string) (model.Plan, error) {
	ref = strings.TrimSpace(ref)
	plans, err := s.Store.Plans(ctx)
	if err != nil {
		return model.Plan{}, err
	}
	for _, p := range plans {
		if p.ID == ref {
			return p, nil
		}
	}
	for _, p := range plans {
		if strings.EqualFold(p.Name, ref) {
			return p, nil
		}
	}
	return model.Plan{}, errors.New("no plan with ID or name " + strconv.Quote(ref))
}

// accountByExternalID finds the account a billing system knows by its ID.
func (s *Server) accountByExternalID(ctx context.Context, ext string) (model.Account, error) {
	if ext == "" {
		return model.Account{}, store.ErrNotFound
	}
	accounts, err := s.Store.Accounts(ctx)
	if err != nil {
		return model.Account{}, err
	}
	for _, a := range accounts {
		if a.ExternalID == ext {
			return a, nil
		}
	}
	return model.Account{}, store.ErrNotFound
}

// provAccount resolves the {id} of a provisioning route.
func (s *Server) provAccount(w http.ResponseWriter, r *http.Request) (model.Account, bool) {
	id := r.PathValue("id")
	var a model.Account
	var err error
	if ext, ok := strings.CutPrefix(id, "ext:"); ok {
		a, err = s.accountByExternalID(r.Context(), ext)
	} else {
		a, err = s.Store.Account(r.Context(), id)
	}
	if err != nil {
		writeStoreErr(w, err)
		return a, false
	}
	return a, true
}

var usernameClean = regexp.MustCompile(`[^a-z0-9._-]+`)

// freeUsername turns a wish into a username nobody has yet.
func (s *Server) freeUsername(ctx context.Context, wish string) string {
	base := usernameClean.ReplaceAllString(strings.ToLower(wish), "")
	if len(base) > 32 {
		base = base[:32]
	}
	if len(base) < 2 {
		base = "customer"
	}
	name := base
	for i := 2; ; i++ {
		if _, err := s.Store.UserByName(ctx, name); err != nil {
			return name
		}
		name = base + strconv.Itoa(i)
	}
}

// newPassword makes a password a person can copy from an email: letters
// and digits only.
func newPassword() string {
	const set = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 0, 20)
	buf := make([]byte, 64)
	for len(out) < 20 {
		_, _ = rand.Read(buf)
		for _, c := range buf {
			// Skip the top values so every character is equally likely.
			if int(c) < 256/len(set)*len(set) && len(out) < 20 {
				out = append(out, set[int(c)%len(set)])
			}
		}
	}
	return string(out)
}

func (s *Server) provPlans(w http.ResponseWriter, r *http.Request) { s.listPlans(w, r) }

func (s *Server) provListAccounts(w http.ResponseWriter, r *http.Request) {
	if ext := r.URL.Query().Get("external_id"); ext != "" {
		a, err := s.accountByExternalID(r.Context(), ext)
		if err != nil {
			writeJSON(w, 200, []accountView{})
			return
		}
		v, err := s.accountView(r.Context(), a, s.sitesOrNil(r.Context()))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, []accountView{v})
		return
	}
	s.listAccounts(w, r)
}

func (s *Server) sitesOrNil(ctx context.Context) []model.Site {
	sites, _ := s.Store.Sites(ctx)
	return sites
}

func (s *Server) provGetAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.provAccount(w, r)
	if !ok {
		return
	}
	v, err := s.accountView(r.Context(), a, s.sitesOrNil(r.Context()))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) provCreateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		Username   string `json:"username"`
		Password   string `json:"password"`
		Plan       string `json:"plan"` // plan ID or name
		PlanID     string `json:"plan_id"`
		ExternalID string `json:"external_id"`
		Notes      string `json:"notes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a := model.Account{ID: store.NewID(), Name: req.Name, Email: req.Email, Notes: req.Notes, ExternalID: req.ExternalID, CreatedAt: time.Now().UTC()}
	if a.Name == "" {
		a.Name = req.Email
	}
	if err := a.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if a.Email == "" {
		writeErr(w, 400, "the customer's email is needed")
		return
	}
	if ref := req.Plan + req.PlanID; ref != "" {
		if req.Plan != "" && req.PlanID != "" {
			writeErr(w, 400, "give plan or plan_id, not both")
			return
		}
		p, err := s.findPlan(ctx, ref)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		a.PlanID = p.ID
	}
	if a.ExternalID != "" {
		if cur, err := s.accountByExternalID(ctx, a.ExternalID); err == nil {
			writeJSON(w, 409, map[string]any{"error": "an account with this external ID exists", "account_id": cur.ID})
			return
		}
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		local, _, _ := strings.Cut(a.Email, "@")
		username = s.freeUsername(ctx, local)
	}
	password, generated := req.Password, false
	if password == "" {
		password, generated = newPassword(), true
	}
	u, err := newUser(userRequest{Username: username, Email: a.Email, Password: password, Role: model.RoleCustomer}, a.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := s.Store.UserByName(ctx, u.Username); err == nil {
		writeErr(w, 409, store.ErrNameTaken.Error())
		return
	}
	if err := s.Store.SaveAccount(ctx, a); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.Store.SaveUser(ctx, u); err != nil {
		_ = s.Store.DeleteAccount(ctx, a.ID)
		writeErr(w, 400, err.Error())
		return
	}
	s.Store.AddEvent(ctx, "info", "account", "", actor(r)+" created account "+a.Name)
	v, _ := s.accountView(ctx, a, nil)
	out := map[string]any{"account": v, "username": u.Username, "login_url": panelURL(r) + "/login"}
	if generated {
		out["password"] = password
	}
	writeJSON(w, 201, out)
}

// provUpdateAccount changes the plan or the details. Fields left out stay.
func (s *Server) provUpdateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, ok := s.provAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		Name       *string `json:"name"`
		Email      *string `json:"email"`
		Notes      *string `json:"notes"`
		Plan       *string `json:"plan"`
		PlanID     *string `json:"plan_id"`
		ExternalID *string `json:"external_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	next := cur
	if req.Name != nil {
		next.Name = *req.Name
	}
	if req.Email != nil {
		next.Email = *req.Email
	}
	if req.Notes != nil {
		next.Notes = *req.Notes
	}
	if req.ExternalID != nil {
		next.ExternalID = *req.ExternalID
	}
	if req.Plan != nil && req.PlanID != nil {
		writeErr(w, 400, "give plan or plan_id, not both")
		return
	}
	if ref := req.Plan; ref != nil || req.PlanID != nil {
		if ref == nil {
			ref = req.PlanID
		}
		if *ref == "" {
			next.PlanID = ""
		} else {
			p, err := s.findPlan(ctx, *ref)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			next.PlanID = p.ID
		}
	}
	if err := next.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if next.ExternalID != "" && next.ExternalID != cur.ExternalID {
		if other, err := s.accountByExternalID(ctx, next.ExternalID); err == nil && other.ID != cur.ID {
			writeErr(w, 409, "another account has this external ID")
			return
		}
	}
	if err := s.Store.SaveAccount(ctx, next); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(ctx, "info", "account", "", actor(r)+" updated account "+next.Name)
	if next.PlanID != cur.PlanID {
		if err := s.Engine.ApplyPlanLimits(ctx, next.ID); err != nil {
			writeErr(w, 500, "saved, but applying the plan failed: "+err.Error())
			return
		}
	}
	v, _ := s.accountView(ctx, next, s.sitesOrNil(ctx))
	writeJSON(w, 200, v)
}

func (s *Server) provSuspend(suspend bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		a, ok := s.provAccount(w, r)
		if !ok {
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := readOptionalJSON(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if suspend && a.ID == currentUser(r).AccountID {
			writeErr(w, 400, "you cannot suspend your own account")
			return
		}
		if a.Suspended != suspend {
			a.Suspended = suspend
			if err := s.Store.SaveAccount(ctx, a); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			msg := " unsuspended account "
			if suspend {
				msg = " suspended account "
				users, _ := s.Store.Users(ctx, a.ID)
				for _, u := range users {
					s.Store.DeleteUserSessions(ctx, u.ID)
				}
			}
			if req.Reason = strings.TrimSpace(req.Reason); req.Reason != "" {
				msg = msg + a.Name + ": " + req.Reason
			} else {
				msg += a.Name
			}
			s.Store.AddEvent(ctx, "info", "account", "", actor(r)+msg)
		}
		writeJSON(w, 200, a)
	}
}

// customerLogin returns the account's first customer login.
func (s *Server) customerLogin(ctx context.Context, accountID string) (model.User, error) {
	users, err := s.Store.Users(ctx, accountID)
	if err != nil {
		return model.User{}, err
	}
	for _, u := range users {
		if !u.IsAdmin() {
			return u, nil
		}
	}
	return model.User{}, errors.New("the account has no customer login")
}

func (s *Server) provPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, ok := s.provAccount(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readOptionalJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.customerLogin(ctx, a.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	password, generated := req.Password, false
	if password == "" {
		password, generated = newPassword(), true
	}
	h, err := auth.HashPassword(password)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u.PasswordHash = h
	if err := s.Store.SaveUser(ctx, u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.DeleteUserSessions(ctx, u.ID)
	s.Store.AddEvent(ctx, "info", "account", "", actor(r)+" reset the password of "+u.Username)
	out := map[string]any{"username": u.Username}
	if generated {
		out["password"] = password
	}
	writeJSON(w, 200, out)
}

// One-time sign-in links ------------------------------------------------------

const ssoTTL = 5 * time.Minute

type ssoGrant struct {
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
}

func ssoKey(tok string) string { return "sso:" + tok }

// panelURL is the address the request reached Berth at.
func panelURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "http" {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

func (s *Server) provLoginLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, ok := s.provAccount(w, r)
	if !ok {
		return
	}
	if a.Suspended {
		writeErr(w, 400, "the account is suspended")
		return
	}
	u, err := s.customerLogin(ctx, a.ID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tok := auth.RandomToken(32)
	exp := time.Now().UTC().Add(ssoTTL)
	if err := s.Store.SetKV(ctx, ssoKey(tok), ssoGrant{User: u.ID, Expires: exp}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	path := "/api/sso/" + tok
	writeJSON(w, 200, map[string]any{"url": panelURL(r) + path, "path": path, "expires": exp, "username": u.Username})
}

// sso signs a customer in with a one-time link and opens the panel.
func (s *Server) sso(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := r.PathValue("token")
	var g ssoGrant
	_ = s.Store.GetKV(ctx, ssoKey(tok), &g)
	_ = s.Store.DeleteKV(ctx, ssoKey(tok))
	fail := func() { http.Redirect(w, r, "/login?link=expired", http.StatusSeeOther) }
	if tok == "" || g.User == "" || time.Now().After(g.Expires) {
		fail()
		return
	}
	u, err := s.Store.UserByID(ctx, g.User)
	if err != nil {
		fail()
		return
	}
	if a, err := s.Store.Account(ctx, u.AccountID); err != nil || a.Suspended {
		fail()
		return
	}
	if err := s.setSession(w, r, u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(ctx, "info", "auth", "", u.Username+" signed in with a one-time link")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Termination -----------------------------------------------------------------

// provTerminate deletes an account. With ?delete_sites=1 its sites are
// deleted first (each with a final backup); otherwise an account that still
// has sites is refused.
func (s *Server) provTerminate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, ok := s.provAccount(w, r)
	if !ok {
		return
	}
	if a.ID == currentUser(r).AccountID {
		writeErr(w, 400, "you cannot delete your own account")
		return
	}
	users, _ := s.Store.Users(ctx, a.ID)
	for _, u := range users {
		if u.IsAdmin() {
			writeErr(w, 400, "this account has an admin ("+u.Username+"); remove them first")
			return
		}
	}
	var mine []model.Site
	for _, site := range s.sitesOrNil(ctx) {
		if site.AccountID == a.ID {
			mine = append(mine, site)
		}
	}
	who := actor(r)
	if len(mine) == 0 {
		if err := s.Store.DeleteAccount(ctx, a.ID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.Store.AddEvent(ctx, "info", "account", "", who+" deleted account "+a.Name)
		writeJSON(w, 200, map[string]any{"status": "deleted"})
		return
	}
	if v := r.URL.Query().Get("delete_sites"); v != "1" && v != "true" {
		writeJSON(w, 409, map[string]any{"error": "the account still has sites; pass delete_sites=1 to delete them too", "sites": siteNames(mine)})
		return
	}
	// Lock the customer out while their sites go.
	if !a.Suspended {
		a.Suspended = true
		_ = s.Store.SaveAccount(ctx, a)
		for _, u := range users {
			s.Store.DeleteUserSessions(ctx, u.ID)
		}
	}
	var jobs []int64
	for _, site := range mine {
		if site.Status == model.StatusDeleting {
			continue
		}
		id, err := s.Engine.DeleteSite(ctx, site.Name, true, who)
		if err != nil {
			writeErr(w, 500, "deleting "+site.Name+": "+err.Error())
			return
		}
		jobs = append(jobs, id)
	}
	go s.finishTermination(a, who, jobs)
	writeJSON(w, 202, map[string]any{"status": "terminating", "jobs": jobs, "sites": siteNames(mine)})
}

// finishTermination deletes the account once its sites' delete jobs end.
func (s *Server) finishTermination(a model.Account, who string, jobs []int64) {
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Hour)
	for {
		running := 0
		for _, id := range jobs {
			if j, err := s.Store.Job(ctx, id); err == nil && j.Status != "ok" && j.Status != "failed" {
				running++
			}
		}
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			s.Store.AddEvent(ctx, "error", "account", "", "Terminating "+a.Name+" timed out; the account stays suspended.")
			return
		}
		time.Sleep(time.Second)
	}
	// Sites still marked deleting may wait on another job (one being created).
	for wait := 0; wait < 120; wait++ {
		left, deleting := 0, 0
		for _, site := range s.sitesOrNil(ctx) {
			if site.AccountID == a.ID {
				left++
				if site.Status == model.StatusDeleting {
					deleting++
				}
			}
		}
		if left == 0 {
			if err := s.Store.DeleteAccount(ctx, a.ID); err == nil {
				s.Store.AddEvent(ctx, "info", "account", "", who+" deleted account "+a.Name)
			}
			return
		}
		if deleting == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	s.Store.AddEvent(ctx, "error", "account", "", "Terminating "+a.Name+" stopped: a site could not be deleted (see Jobs). The account stays suspended.")
}
