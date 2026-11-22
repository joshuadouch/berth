// Package plugins lists the plugins compiled into Berth.
package plugins

import (
	"context"
	"strings"

	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins/cloudflare"
	"github.com/joshuadouch/berth/internal/plugins/halyard"
	"github.com/joshuadouch/berth/internal/plugins/mailcow"
	"github.com/joshuadouch/berth/internal/store"
)

// All returns a fresh instance of every plugin.
func All() []plugin.Plugin {
	return []plugin.Plugin{mailcow.New(), cloudflare.New(), halyard.New()}
}

// Migrate turns settings from before Halyard and Cloudflare were plugins into
// plugin state: a panel that used them gets the plugin switched on for the
// admin's account (and every account with a published site).
func Migrate(ctx context.Context, st *store.Store, reg *plugin.Registry) error {
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	admin, err := st.FirstUser(ctx)
	if err != nil {
		return nil // no admin yet: nothing to move
	}
	if !reg.Saved(ctx, halyard.ID) {
		accounts := []string{}
		sites, _ := st.Sites(ctx)
		used := settings.Halyard.URL != ""
		for _, s := range sites {
			if s.Halyard.Enabled || s.Halyard.HostID != "" {
				used = true
				accounts = append(accounts, s.AccountID)
			}
		}
		if used {
			accounts = append(accounts, admin.AccountID)
			if _, err := reg.Save(ctx, halyard.ID, true, nil); err != nil {
				return err
			}
			if err := reg.SetAccounts(ctx, halyard.ID, accounts); err != nil {
				return err
			}
		}
	}
	if !reg.Saved(ctx, cloudflare.ID) && (settings.CloudflareToken != "" || settings.AccessTeam != "") {
		cfg := plugin.Config{
			"token": settings.CloudflareToken, "access_team": settings.AccessTeam, "access_aud": settings.AccessAUD,
			"access_emails": strings.Join(settings.AccessEmails, "\n"),
		}
		if _, err := reg.Save(ctx, cloudflare.ID, true, cfg); err != nil {
			return err
		}
		if err := reg.SetAccounts(ctx, cloudflare.ID, []string{admin.AccountID}); err != nil {
			return err
		}
		settings.CloudflareToken, settings.AccessTeam, settings.AccessAUD, settings.AccessEmails = "", "", "", nil
		if err := st.SetSettings(ctx, settings); err != nil {
			return err
		}
	}
	return nil
}
