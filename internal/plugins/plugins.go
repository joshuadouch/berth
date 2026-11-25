// Package plugins lists the plugins compiled into Berth.
package plugins

import (
	"context"
	"strings"

	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins/cloudflare"
	"github.com/joshuadouch/berth/internal/plugins/dns"
	"github.com/joshuadouch/berth/internal/plugins/mailcow"
	"github.com/joshuadouch/berth/internal/store"
)

// All returns a fresh instance of every plugin.
func All() []plugin.Plugin {
	return []plugin.Plugin{mailcow.New(), cloudflare.New(), dns.New()}
}

// Migrate turns settings from before Cloudflare was a plugin into plugin
// state (a panel that used it gets the plugin switched on for the admin's
// account), and drops what the removed Halyard plugin left behind.
func Migrate(ctx context.Context, st *store.Store, reg *plugin.Registry) error {
	if err := dropHalyard(ctx, st); err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	admin, err := st.FirstUser(ctx)
	if err != nil {
		return nil // no admin yet: nothing to move
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

// dropHalyard removes the Halyard plugin's state and re-saves the settings and
// every site once, so the fields Halyard used are gone from the database too.
func dropHalyard(ctx context.Context, st *store.Store) error {
	var done bool
	if err := st.GetKV(ctx, "halyard_removed", &done); err == nil && done {
		return nil
	}
	if err := st.DeleteKV(ctx, "plugin:halyard"); err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if err := st.SetSettings(ctx, settings); err != nil {
		return err
	}
	sites, err := st.Sites(ctx)
	if err != nil {
		return err
	}
	for _, s := range sites {
		if err := st.SaveSite(ctx, s); err != nil {
			return err
		}
	}
	return st.SetKV(ctx, "halyard_removed", true)
}
