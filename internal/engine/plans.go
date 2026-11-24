package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/joshuadouch/berth/internal/model"
)

// AccountUsage returns an account's last measured disk use in bytes and its
// number of sites.
func (e *Engine) AccountUsage(ctx context.Context, account string) (int64, int) {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return 0, 0
	}
	usage := e.SiteUsage()
	var total int64
	n := 0
	for _, s := range sites {
		if s.AccountID != account || s.Status == model.StatusDeleting {
			continue
		}
		n++
		total += usage[s.Name].Files + usage[s.Name].DB
	}
	return total, n
}

// OverQuota returns an error when an account has used up its plan's disk.
func (e *Engine) OverQuota(ctx context.Context, account string) error {
	p, ok := e.Store.AccountPlan(ctx, account)
	if !ok || p.DiskMB == 0 {
		return nil
	}
	used, _ := e.AccountUsage(ctx, account)
	if used >= int64(p.DiskMB)<<20 {
		return fmt.Errorf("the account has used its %s of disk space (plan %s); free some space or ask for a bigger plan", mb(p.DiskMB), p.Name)
	}
	return nil
}

func mb(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return fmt.Sprintf("%d GB", n/1024)
	}
	return fmt.Sprintf("%d MB", n)
}

// applyPlan holds a site to its account's plan: resource values are capped
// (and default to the plan's), and new sites must fit the site count and disk.
func (e *Engine) applyPlan(ctx context.Context, s *model.Site, isNew bool) error {
	p, ok := e.Store.AccountPlan(ctx, s.AccountID)
	if !ok {
		return nil
	}
	s.MemoryMB = model.Limit(s.MemoryMB, p.MemoryMB)
	s.MaxChildren = model.Limit(s.MaxChildren, p.Workers)
	s.UploadMB = model.Limit(s.UploadMB, p.UploadMB)
	if p.NoSFTP {
		s.SFTP.Enabled = false
	}
	if p.MaxDomains > 0 && len(s.Hostnames()) > p.MaxDomains {
		return fmt.Errorf("plan %s allows %d domain%s per site", p.Name, p.MaxDomains, plural(p.MaxDomains))
	}
	if !isNew {
		return nil
	}
	if p.NoSites {
		return fmt.Errorf("plan %s does not include websites", p.Name)
	}
	if _, n := e.AccountUsage(ctx, s.AccountID); p.MaxSites > 0 && n >= p.MaxSites {
		return fmt.Errorf("plan %s allows %d site%s, and the account has %d", p.Name, p.MaxSites, plural(p.MaxSites), n)
	}
	return e.OverQuota(ctx, s.AccountID)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ApplyPlanLimits brings an account's existing sites within its plan, after
// the plan or the account's choice of plan changed.
func (e *Engine) ApplyPlanLimits(ctx context.Context, account string) error {
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return err
	}
	p, hasPlan := e.Store.AccountPlan(ctx, account)
	for _, s := range sites {
		if s.AccountID != account || s.Status == model.StatusDeleting {
			continue
		}
		unlock := e.lockSite(s.ID)
		cur, err := e.Store.Site(ctx, s.ID)
		if err == nil {
			next := cur
			if hasPlan {
				next.MemoryMB = model.Limit(next.MemoryMB, p.MemoryMB)
				next.MaxChildren = model.Limit(next.MaxChildren, p.Workers)
				next.UploadMB = model.Limit(next.UploadMB, p.UploadMB)
				if p.NoSFTP {
					next.SFTP.Enabled = false
				}
			}
			if next.MemoryMB != cur.MemoryMB || next.MaxChildren != cur.MaxChildren || next.UploadMB != cur.UploadMB || next.SFTP.Enabled != cur.SFTP.Enabled {
				if err := e.Store.SaveSite(ctx, next); err == nil {
					if next.SFTP.Enabled != cur.SFTP.Enabled {
						_ = e.syncSFTP(ctx, next)
					}
				}
			}
			if next.DB != nil && next.Status == model.StatusActive {
				conns := 0
				if hasPlan {
					conns = p.DBConnections
				}
				_ = e.dbLimit(ctx, *next.DB, conns)
			}
		}
		unlock()
	}
	// Apply also brings disable_functions in line with the plan.
	return e.Apply(ctx)
}

// dbLimit caps a site database user's connections (0 = no cap).
func (e *Engine) dbLimit(ctx context.Context, db model.Database, conns int) error {
	if !model.ValidName(strings.TrimPrefix(db.Name, "site_")) {
		return fmt.Errorf("invalid database name")
	}
	_, err := e.sql(ctx, fmt.Sprintf("ALTER USER '%s'@'localhost' WITH MAX_USER_CONNECTIONS %d;\n", db.Name, conns))
	return err
}
