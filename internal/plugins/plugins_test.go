package plugins

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins/cloudflare"
	"github.com/joshuadouch/berth/internal/plugins/halyard"
	"github.com/joshuadouch/berth/internal/store"
)

func TestMigrate(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.SaveUser(ctx, model.User{ID: "u1", Username: "josh", Email: "j@x.y"})
	_ = st.SaveSite(ctx, model.Site{ID: "a", Name: "a", Halyard: model.HalyardLink{Enabled: true, HostID: "h1"}})
	if err := st.MigrateAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := st.Settings(ctx)
	s.Halyard.URL, s.CloudflareToken, s.AccessTeam, s.AccessAUD, s.AccessEmails = "https://h:8443", "tok", "team", "aud", []string{"a@b.c"}
	_ = st.SetSettings(ctx, s)
	reg := plugin.NewRegistry(st, nil, All()...)
	if err := Migrate(ctx, st, reg); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, "u1")
	if !reg.ActiveFor(ctx, halyard.ID, u.AccountID) || !reg.ActiveFor(ctx, cloudflare.ID, u.AccountID) {
		t.Fatal("plugins not on for the admin's account")
	}
	c, _ := reg.Config(ctx, cloudflare.ID)
	team, aud, emails := cloudflare.Access(c)
	if c.Str("token") != "tok" || team != "team" || aud != "aud" || len(emails) != 1 {
		t.Fatalf("cloudflare config %v", c)
	}
	if s, _ := st.Settings(ctx); s.CloudflareToken != "" || s.AccessTeam != "" {
		t.Fatal("old Cloudflare settings left behind")
	}
	// Running again changes nothing, even after the admin turns a plugin off.
	_ = reg.SetAccounts(ctx, halyard.ID, nil)
	if err := Migrate(ctx, st, reg); err != nil {
		t.Fatal(err)
	}
	if reg.ActiveFor(ctx, halyard.ID, u.AccountID) {
		t.Fatal("migration ran twice")
	}
}
