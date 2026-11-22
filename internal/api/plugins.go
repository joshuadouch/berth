package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins/cloudflare"
)

func pluginErr(w http.ResponseWriter, err error) {
	if errors.Is(err, plugin.ErrUnknown) {
		writeErr(w, 404, err.Error())
		return
	}
	writeErr(w, 400, err.Error())
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	list, err := s.Plugins.List(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) savePlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool          `json:"enabled"`
		Config  plugin.Config `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	before := s.Plugins.Enabled(r.Context(), id)
	v, err := s.Plugins.Save(r.Context(), id, req.Enabled, req.Config)
	if err != nil {
		pluginErr(w, err)
		return
	}
	switch {
	case req.Enabled && !before:
		s.Store.AddEvent(r.Context(), "info", "plugin", "", "Turned on the "+v.Name+" plugin")
	case !req.Enabled && before:
		s.Store.AddEvent(r.Context(), "info", "plugin", "", "Turned off the "+v.Name+" plugin")
	default:
		s.Store.AddEvent(r.Context(), "info", "plugin", "", "Changed the "+v.Name+" plugin settings")
	}
	writeJSON(w, 200, v)
}

func (s *Server) testPlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config plugin.Config `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg, err := s.Plugins.Test(r.Context(), r.PathValue("id"), req.Config)
	if err != nil {
		pluginErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}

// accessSettings returns the Cloudflare Access sign-in settings, which live
// in the Cloudflare plugin.
func (s *Server) accessSettings(ctx context.Context) (team, aud string, emails []string) {
	if !s.Plugins.Enabled(ctx, cloudflare.ID) {
		return "", "", nil
	}
	c, err := s.Plugins.Config(ctx, cloudflare.ID)
	if err != nil {
		return "", "", nil
	}
	return cloudflare.Access(c)
}

// pluginAccounts chooses the accounts a per-account plugin is on for. Admin
// only plugins can only go to accounts with an admin login.
func (s *Server) pluginAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Accounts []string `json:"accounts"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	info, err := s.Plugins.Info(id)
	if err != nil {
		pluginErr(w, err)
		return
	}
	names := []string{}
	for _, a := range req.Accounts {
		acct, err := s.Store.Account(r.Context(), a)
		if err != nil {
			writeErr(w, 400, "no such account "+a)
			return
		}
		if info.AdminOnly && !s.hasAdmin(r.Context(), a) {
			writeErr(w, 400, info.Name+" is only for your own accounts, not for hosting customers ("+acct.Name+" has no admin login)")
			return
		}
		names = append(names, acct.Name)
	}
	if err := s.Plugins.SetAccounts(r.Context(), id, req.Accounts); err != nil {
		pluginErr(w, err)
		return
	}
	msg := "nobody"
	if len(names) > 0 {
		msg = strings.Join(names, ", ")
	}
	s.Store.AddEvent(r.Context(), "info", "plugin", "", currentUser(r).Username+" turned the "+info.Name+" plugin on for "+msg)
	v, err := s.Plugins.View(r.Context(), id)
	if err != nil {
		pluginErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

// hasAdmin reports whether an account has an admin login.
func (s *Server) hasAdmin(ctx context.Context, account string) bool {
	users, _ := s.Store.Users(ctx, account)
	for _, u := range users {
		if u.IsAdmin() {
			return true
		}
	}
	return false
}

// pluginGate lets admins use every enabled plugin's API, and customers only
// plugins that are on for their account and meant for customers.
func (s *Server) pluginGate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if !u.IsAdmin() {
			id := r.PathValue("id")
			info, err := s.Plugins.Info(id)
			if err != nil || info.AdminOnly || !s.Plugins.ActiveFor(r.Context(), id, u.AccountID) {
				writeErr(w, 404, "no such plugin")
				return
			}
		}
		h(w, r.WithContext(plugin.WithUser(r.Context(), u)))
	}
}

// pluginLink is a plugin page in a customer's menu.
type pluginLink struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	NavLabel string `json:"nav_label"`
}

// myPlugins lists the plugins a customer may use (admins load /api/plugins).
func (s *Server) myPlugins(r *http.Request) []pluginLink {
	out := []pluginLink{}
	u := currentUser(r)
	if u.IsAdmin() {
		return out
	}
	list, _ := s.Plugins.List(r.Context())
	for _, v := range list {
		if !v.AdminOnly && s.Plugins.ActiveFor(r.Context(), v.ID, u.AccountID) {
			out = append(out, pluginLink{ID: v.ID, Name: v.Name, Icon: v.Icon, NavLabel: v.NavLabel})
		}
	}
	return out
}
