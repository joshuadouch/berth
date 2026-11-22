package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/joshuadouch/berth/internal/plugin"
)

// ID is the plugin's ID.
const ID = "cloudflare"

// Plugin is the Cloudflare plugin.
type Plugin struct {
	host *plugin.Host
}

// New returns the plugin.
func New() *Plugin { return &Plugin{} }

// Info implements plugin.Plugin.
func (p *Plugin) Info() plugin.Info {
	return plugin.Info{
		ID:          ID,
		Name:        "Cloudflare",
		Description: "Clear Cloudflare's cache from a site's page, and sign in to this panel with Cloudflare Access. For your own accounts only: it is never offered to hosting customers.",
		Version:     "1.0",
		Icon:        "cloud",
		PerAccount:  true,
		AdminOnly:   true,
		Fields: []plugin.Field{
			{Key: "token", Label: "API token", Type: plugin.Secret, Section: "Cache", Help: "A token with Zone → Cache Purge on your zones. Sites of the accounts this plugin is on for get a Clear cache button."},
			{Key: "access_team", Label: "Team domain", Type: plugin.Text, Section: "Cloudflare Access sign-in", Placeholder: "example.cloudflareaccess.com", Help: "Publish this panel through Cloudflare Access and admins are signed in by Access instead of a password."},
			{Key: "access_aud", Label: "Application audience (AUD) tag", Type: plugin.Text, Section: "Cloudflare Access sign-in"},
			{Key: "access_emails", Label: "Extra allowed emails", Type: plugin.TextArea, Section: "Cloudflare Access sign-in", Help: "One per line. They sign in as the first admin. The first admin's own email is always allowed."},
		},
	}
}

// Test implements plugin.Tester.
func (p *Plugin) Test(ctx context.Context, c plugin.Config) (string, error) {
	if c.Str("token") == "" {
		return "", errors.New("enter an API token first")
	}
	return (&Client{Token: c.Str("token")}).Verify(ctx)
}

// Access returns the Cloudflare Access settings, or empty strings when they
// are not set.
func Access(c plugin.Config) (team, aud string, emails []string) {
	for _, e := range strings.Split(c.Str("access_emails"), "\n") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			emails = append(emails, e)
		}
	}
	return c.Str("access_team"), c.Str("access_aud"), emails
}

// Init implements plugin.Plugin.
func (p *Plugin) Init(h *plugin.Host) http.Handler {
	p.host = h
	m := http.NewServeMux()
	m.HandleFunc("POST /sites/{name}/purge", p.purge)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { plugin.WriteErr(w, 404, "no such endpoint") })
	return m
}

func (p *Plugin) purge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	site, err := p.host.Site(ctx, r.PathValue("name"))
	if err != nil {
		plugin.WriteErr(w, 404, "no such site")
		return
	}
	if !p.host.ActiveFor(ctx, site.AccountID) {
		plugin.WriteErr(w, 403, "the Cloudflare plugin is not on for this site's account")
		return
	}
	c, err := p.host.Config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	if c.Str("token") == "" {
		plugin.WriteErr(w, 400, "add an API token to the Cloudflare plugin first")
		return
	}
	n, err := (&Client{Token: c.Str("token")}).Purge(ctx, site.Hostnames())
	if err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	p.host.SiteEvent(ctx, "info", site.Name, fmt.Sprintf("%s cleared Cloudflare's cache for %s", plugin.User(r).Username, site.Domain))
	plugin.WriteJSON(w, 200, map[string]any{"ok": true, "hosts": n})
}
