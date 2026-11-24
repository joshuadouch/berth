package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// planUsage is an account's plan and how much of it is used.
type planUsage struct {
	Plan     *model.Plan `json:"plan"`
	DiskUsed int64       `json:"disk_used"`
	Sites    int         `json:"sites"`
}

func (s *Server) planUsage(ctx context.Context, account string) planUsage {
	u := planUsage{}
	if p, ok := s.Store.AccountPlan(ctx, account); ok {
		u.Plan = &p
	}
	u.DiskUsed, u.Sites = s.Engine.AccountUsage(ctx, account)
	return u
}

type planView struct {
	model.Plan
	Accounts int `json:"accounts"`
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.Store.Plans(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	accounts, _ := s.Store.Accounts(r.Context())
	out := make([]planView, 0, len(plans))
	for _, p := range plans {
		v := planView{Plan: p}
		for _, a := range accounts {
			if a.PlanID == p.ID || slices.Contains(a.AddonPlans, p.ID) {
				v.Accounts++
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) savePlan(w http.ResponseWriter, r *http.Request) {
	var p model.Plan
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	if id := r.PathValue("id"); id != "" {
		if _, err := s.Store.Plan(ctx, id); err != nil {
			writeStoreErr(w, err)
			return
		}
		p.ID = id
	} else {
		p.ID = store.NewID()
	}
	s.commitPlan(w, r, p, 200)
}

// commitPlan validates and stores a plan, then brings its accounts in line.
func (s *Server) commitPlan(w http.ResponseWriter, r *http.Request, p model.Plan, status int) {
	ctx := r.Context()
	if err := p.Normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if plans, err := s.Store.Plans(ctx); err == nil {
		for _, o := range plans {
			if o.ID != p.ID && strings.EqualFold(o.Name, p.Name) {
				writeErr(w, 409, "another plan is already called "+o.Name)
				return
			}
		}
	}
	if err := s.Store.SavePlan(ctx, p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(ctx, "info", "plan", "", currentUser(r).Username+" saved plan "+p.Name)
	// Bring the plan's accounts in line in the background.
	accounts, _ := s.Store.Accounts(ctx)
	go func() {
		for _, a := range accounts {
			if a.PlanID == p.ID {
				if err := s.Engine.ApplyPlanLimits(context.Background(), a.ID); err != nil {
					s.Log.Warn("apply plan", "account", a.ID, "err", err)
				}
			}
		}
	}()
	writeJSON(w, status, p)
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.Plan(ctx, r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	accounts, _ := s.Store.Accounts(ctx)
	for _, a := range accounts {
		if a.PlanID == p.ID {
			writeErr(w, 400, "move "+a.Name+" to another plan first")
			return
		}
		if slices.Contains(a.AddonPlans, p.ID) {
			writeErr(w, 400, a.Name+" has this plan as an add-on; take it off first")
			return
		}
	}
	if err := s.Store.DeletePlan(ctx, p.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
