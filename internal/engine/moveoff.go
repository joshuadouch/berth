package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

// halyardGrace is how long Halyard keeps answering for a site after its DNS
// points at Berth, while resolvers and Cloudflare catch up.
const halyardGrace = 15 * time.Minute

// DNSKeepers tells the engine whether a plugin keeps a site's DNS pointing at
// this server (the registry implements it).
type DNSKeepers interface {
	PointsHere(ctx context.Context, s model.Site) bool
}

func (e *Engine) pointsHere(ctx context.Context, s model.Site) bool {
	k, ok := e.Hooks.(DNSKeepers)
	return ok && k.PointsHere(ctx, s)
}

// MoveOffHalyard starts a job that moves a site from Halyard to Berth.
func (e *Engine) MoveOffHalyard(ctx context.Context, name, author string) (int64, error) {
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return 0, err
	}
	if !s.Halyard.Enabled {
		return 0, errors.New("this site is not published on Halyard")
	}
	return e.StartJob(name, "halyard", "Move "+s.Domain+" to Berth", func(ctx context.Context, logf Logf) error {
		err := e.moveOffHalyard(ctx, name, logf)
		if err == nil {
			e.Store.AddEvent(ctx, "info", "halyard", name, author+" moved "+s.Domain+" off Halyard; Berth serves it directly")
		}
		return err
	})
}

// MoveAllOffHalyard starts a job that moves every site on Halyard, one at a
// time. A site that fails stays on Halyard and the rest carry on.
func (e *Engine) MoveAllOffHalyard(ctx context.Context, author string) (int64, error) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, s := range sites {
		if s.Halyard.Enabled && s.Status == model.StatusActive {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return 0, errors.New("no sites are published on Halyard")
	}
	return e.StartJob("", "halyard", fmt.Sprintf("Move %d sites to Berth", len(names)), func(ctx context.Context, logf Logf) error {
		var failed []string
		for _, n := range names {
			logf("── %s", n)
			if err := e.moveOffHalyard(ctx, n, func(f string, a ...any) { logf("   "+f, a...) }); err != nil {
				logf("   %s stays on Halyard: %v", n, err)
				failed = append(failed, n)
				continue
			}
			e.Store.AddEvent(ctx, "info", "halyard", n, author+" moved the site off Halyard; Berth serves it directly")
		}
		if len(failed) > 0 {
			return fmt.Errorf("%d of %d sites stayed on Halyard: %s", len(failed), len(names), strings.Join(failed, ", "))
		}
		return nil
	})
}

// moveOffHalyard moves one site without a gap for visitors:
//
//  1. Berth takes over HTTPS for the site while Halyard still sends it
//     traffic (nginx answers Halyard's plain HTTP as before).
//  2. Berth gets its own certificate: through Cloudflare DNS when the
//     Cloudflare plugin can, else over HTTP through Halyard.
//  3. The Cloudflare plugin points the site's records at this server.
//  4. Halyard's host is removed after a grace period, or when someone
//     presses Remove if DNS is not managed here.
//
// If the certificate cannot be had, the site goes back to Halyard as it was.
func (e *Engine) moveOffHalyard(ctx context.Context, name string, logf Logf) error {
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return err
	}
	if !s.Halyard.Enabled {
		logf("Already off Halyard")
		return nil
	}
	if s.Status != model.StatusActive {
		return errors.New("the site is not active")
	}
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return err
	}
	if s.HTTPS && st.ACME.Off {
		return errors.New("Let's Encrypt is turned off in Settings, so Berth cannot serve HTTPS itself")
	}

	unlock := e.lockSite(name)
	s, err = e.Store.Site(ctx, name)
	if err != nil {
		unlock()
		return err
	}
	old := s
	s.Halyard.Enabled = false
	s.Halyard.MovedAt = time.Now().UTC()
	s.Halyard.RemoveAt = time.Time{}
	if s.HTTPS {
		s.TLSMode = model.TLSModeLocal
		s.TLS.NextTry = time.Time{}
	}
	err = e.Store.SaveSite(ctx, s)
	unlock()
	if err != nil {
		return err
	}
	revert := func(cause error) error {
		unlock := e.lockSite(name)
		if cur, err := e.Store.Site(ctx, name); err == nil {
			cur.Halyard.Enabled, cur.Halyard.MovedAt, cur.TLSMode = true, time.Time{}, old.TLSMode
			_ = e.Store.SaveSite(ctx, cur)
		}
		unlock()
		_ = e.Apply(ctx)
		return cause
	}
	if err := e.Apply(ctx); err != nil {
		return revert(err)
	}

	if s.HTTPS {
		if s.ServesTLS() {
			logf("Berth already has a certificate for %s", strings.Join(s.Hostnames(), ", "))
		} else if err := e.EnsureCert(ctx, name, true, logf); err != nil {
			logf("No certificate, so the site stays on Halyard")
			return revert(fmt.Errorf("certificate: %w", err))
		}
		logf("Berth now serves HTTPS for %s (and still answers Halyard)", s.Domain)
	}

	cur, err := e.Store.Site(ctx, name)
	if err != nil {
		return err
	}
	e.siteChanged(ctx, &old, &cur, logf)
	unlock = e.lockSite(name)
	defer unlock()
	cur, err = e.Store.Site(ctx, name)
	if err != nil {
		return err
	}
	if e.pointsHere(ctx, cur) {
		cur.Halyard.RemoveAt = time.Now().Add(halyardGrace).UTC()
		logf("DNS points at this server. Halyard keeps answering for %d minutes while caches catch up, then its host for the site is removed", int(halyardGrace/time.Minute))
	} else {
		addr := strings.TrimSpace(st.Brand.IPv4 + " " + st.Brand.IPv6)
		if addr == "" {
			addr = "this server's public address"
		}
		logf("Point DNS for %s at %s, then press Remove from Halyard on the site's page", strings.Join(cur.Hostnames(), ", "), addr)
	}
	return e.Store.SaveSite(ctx, cur)
}

// removeMovedHalyard deletes the Halyard hosts of moved sites whose grace
// period is over.
func (e *Engine) removeMovedHalyard(ctx context.Context, now time.Time) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	for _, s := range sites {
		if s.Halyard.Enabled || s.Halyard.RemoveAt.IsZero() || now.Before(s.Halyard.RemoveAt) {
			continue
		}
		logf := func(f string, a ...any) { e.Log.Info("halyard: "+fmt.Sprintf(f, a...), "site", s.Name) }
		unlock := e.lockSite(s.Name)
		cur, err := e.Store.Site(ctx, s.Name)
		if err != nil || cur.Halyard.Enabled || cur.Halyard.RemoveAt.IsZero() {
			unlock()
			continue
		}
		if err := e.unlinkHalyard(ctx, &cur, logf); err != nil {
			cur.Halyard.RemoveAt = now.Add(10 * time.Minute).UTC()
			cur.Halyard.Error = firstLine(err.Error())
			_ = e.Store.SaveSite(ctx, cur)
			unlock()
			continue
		}
		cur.Halyard = model.HalyardLink{MovedAt: cur.Halyard.MovedAt}
		_ = e.Store.SaveSite(ctx, cur)
		unlock()
		e.Store.AddEvent(ctx, "info", "halyard", s.Name, "Removed "+s.Domain+"'s host from Halyard; Berth serves it directly")
	}
}
