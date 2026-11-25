package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
)

// ID is the plugin's ID.
const ID = "cloudflare"

// Plugin is the Cloudflare plugin.
type Plugin struct {
	host *plugin.Host
	mu   sync.Mutex // one DNS sync at a time
	// HTTP is used for Cloudflare calls (tests).
	HTTP *http.Client
}

// New returns the plugin.
func New() *Plugin { return &Plugin{} }

// Info implements plugin.Plugin.
func (p *Plugin) Info() plugin.Info {
	return plugin.Info{
		ID:          ID,
		Name:        "Cloudflare",
		Description: "Point sites' DNS at this server in Cloudflare and keep it right, get certificates through Cloudflare DNS, and clear Cloudflare's cache from a site's page. Your accounts use your token; hosting customers can connect their own Cloudflare account on the Cloudflare page.",
		Version:     "2.0",
		Icon:        "cloud",
		NavLabel:    "Cloudflare",
		PerAccount:  true,
		Fields: []plugin.Field{
			{Key: "token", Label: "API token", Type: plugin.Secret, Section: "Your accounts", Help: "Used for the accounts you choose below. Give it Zone → Zone → Read, Zone → DNS → Edit and Zone → Cache Purge on your zones."},
			{Key: "dns", Label: "Manage DNS records for sites", Type: plugin.Bool, Section: "DNS", Default: false, Help: "Sites get A and AAAA records pointing at this server's public addresses (Settings → Customers). Records Halyard made are taken over; records anyone else made are left alone until you take them over from the site's page."},
			{Key: "proxied", Label: "Proxy through Cloudflare (orange cloud)", Type: plugin.Bool, Section: "DNS", Default: true, Help: "The default for new records. Each site can change it."},
			{Key: "customers", Label: "Offer to hosting customers", Type: plugin.Bool, Section: "Customers", Default: true, Help: "Customers get a Cloudflare page where they can connect their own Cloudflare account for DNS, certificates and cache clearing."},
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
	return p.client(c.Str("token")).Verify(ctx)
}

// AllowAccount implements plugin.Gate: every account may connect its own
// Cloudflare account while the plugin is offered to customers.
func (p *Plugin) AllowAccount(ctx context.Context, account string) bool {
	c, err := p.host.Config(ctx)
	return err == nil && c.Bool("customers")
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

func (p *Plugin) client(token string) *Client { return &Client{Token: token, HTTP: p.HTTP} }

// accountToken is a customer's own Cloudflare token.
type accountToken struct {
	Token string    `json:"token"`
	Zones []string  `json:"zones,omitempty"`
	Saved time.Time `json:"saved"`
}

func (p *Plugin) ownToken(ctx context.Context, account string) accountToken {
	var t accountToken
	_ = p.host.GetData(ctx, "account:"+account, &t)
	return t
}

// token returns the Cloudflare token for an account: its own, or the
// server's for accounts an admin chose for the plugin.
func (p *Plugin) token(ctx context.Context, account string) (tok string, own bool) {
	if !p.host.ActiveFor(ctx, account) {
		return "", false
	}
	if t := p.ownToken(ctx, account); t.Token != "" {
		return t.Token, true
	}
	if p.host.Chosen(ctx, account) {
		if c, err := p.host.Config(ctx); err == nil {
			return c.Str("token"), false
		}
	}
	return "", false
}

// SiteDNS is the plugin's record of a site's DNS.
type SiteDNS struct {
	// Off leaves the site's records alone.
	Off bool `json:"off"`
	// Proxied overrides the default orange cloud (nil = default).
	Proxied *bool         `json:"proxied,omitempty"`
	Records []RecordState `json:"records"`
	Checked time.Time     `json:"checked,omitempty"`
	Note    string        `json:"note,omitempty"`
}

func (p *Plugin) siteDNS(ctx context.Context, name string) SiteDNS {
	var s SiteDNS
	_ = p.host.GetData(ctx, "site:"+name, &s)
	if s.Records == nil {
		s.Records = []RecordState{}
	}
	return s
}

func (p *Plugin) want(ctx context.Context, c plugin.Config, sd SiteDNS) (Want, error) {
	st, err := p.host.Store.Settings(ctx)
	if err != nil {
		return Want{}, err
	}
	w := Want{IPv4: st.Brand.IPv4, IPv6: st.Brand.IPv6, Proxied: c.Bool("proxied")}
	if sd.Proxied != nil {
		w.Proxied = *sd.Proxied
	}
	if w.IPv4 == "" && w.IPv6 == "" {
		return w, errors.New("set this server's public IPv4 (or IPv6) under Settings → Customers so records have somewhere to point")
	}
	return w, nil
}

// manages reports whether the plugin keeps a site's records, and the token
// to do it with.
func (p *Plugin) manages(ctx context.Context, c plugin.Config, s *model.Site) (string, bool) {
	if s == nil || s.Halyard.Enabled || !c.Bool("dns") {
		return "", false
	}
	tok, _ := p.token(ctx, s.AccountID)
	if tok == "" || p.siteDNS(ctx, s.Name).Off {
		return "", false
	}
	return tok, true
}

// SiteChanged implements plugin.SiteWatcher: it adds, moves and removes the
// records of a site's names.
func (p *Plugin) SiteChanged(ctx context.Context, old, cur *model.Site, logf func(string, ...any)) {
	c, err := p.host.Config(ctx)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	keep := map[string]bool{}
	curTok, curOn := p.manages(ctx, c, cur)
	if curOn {
		for _, n := range cur.Hostnames() {
			keep[strings.ToLower(n)] = true
		}
	}
	if oldTok, oldOn := p.manages(ctx, c, old); oldOn {
		for _, n := range old.Hostnames() {
			if keep[strings.ToLower(n)] && old.AccountID == cur.AccountID {
				continue
			}
			if err := p.client(oldTok).Release(ctx, n); err != nil {
				logf("Cloudflare: could not remove the record for %s: %v", n, err)
			} else {
				logf("Cloudflare: removed the record for %s", n)
			}
		}
	}
	if !curOn {
		return
	}
	p.syncSite(ctx, c, curTok, *cur, false, logf)
}

// syncSite makes a site's records match and saves what it found.
func (p *Plugin) syncSite(ctx context.Context, c plugin.Config, tok string, s model.Site, adopt bool, logf func(string, ...any)) SiteDNS {
	sd := p.siteDNS(ctx, s.Name)
	sd.Records, sd.Note = []RecordState{}, ""
	w, err := p.want(ctx, c, sd)
	if err != nil {
		sd.Note = err.Error()
	} else {
		w.Adopt = adopt
		cl := p.client(tok)
		for _, n := range s.Hostnames() {
			r := cl.Ensure(ctx, n, w)
			sd.Records = append(sd.Records, r)
			switch r.Status {
			case StateOK:
				logf("Cloudflare: %s points at this server", n)
			default:
				logf("Cloudflare: %s: %s", n, r.Detail)
				if r.Status == StateError {
					p.host.SiteEvent(ctx, "error", s.Name, "Cloudflare DNS for "+n+": "+r.Detail)
				}
			}
		}
	}
	sd.Checked = time.Now().UTC()
	_ = p.host.SetData(ctx, "site:"+s.Name, sd)
	return sd
}

// PointsHere implements plugin.DNSKeeper.
func (p *Plugin) PointsHere(ctx context.Context, s model.Site) bool {
	c, err := p.host.Config(ctx)
	if err != nil {
		return false
	}
	if _, ok := p.manages(ctx, c, &s); !ok {
		return false
	}
	ok := map[string]bool{}
	for _, r := range p.siteDNS(ctx, s.Name).Records {
		if r.Status == StateOK {
			ok[strings.ToLower(r.Name)] = true
		}
	}
	for _, n := range s.Hostnames() {
		if !ok[strings.ToLower(n)] {
			return false
		}
	}
	return true
}

// CanChallenge implements plugin.DNSChallenger.
func (p *Plugin) CanChallenge(ctx context.Context, account, name string) bool {
	tok, _ := p.token(ctx, account)
	if tok == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, _, err := p.client(tok).Zone(ctx, strings.TrimPrefix(name, "*."))
	return err == nil
}

// PresentTXT implements plugin.DNSChallenger.
func (p *Plugin) PresentTXT(ctx context.Context, account, fqdn, value string) (func(), error) {
	tok, _ := p.token(ctx, account)
	if tok == "" {
		return nil, errors.New("no Cloudflare token for this account")
	}
	return p.client(tok).PresentTXT(ctx, fqdn, value)
}

// Init implements plugin.Plugin.
func (p *Plugin) Init(h *plugin.Host) http.Handler {
	p.host = h
	m := http.NewServeMux()
	m.HandleFunc("GET /overview", p.overview)
	m.HandleFunc("PUT /account", p.connect)
	m.HandleFunc("DELETE /account", p.disconnect)
	m.HandleFunc("POST /sync", p.syncAll)
	m.HandleFunc("GET /sites/{name}/dns", p.getSiteDNS)
	m.HandleFunc("POST /sites/{name}/dns", p.setSiteDNS)
	m.HandleFunc("POST /sites/{name}/purge", p.purge)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { plugin.WriteErr(w, 404, "no such endpoint") })
	return m
}

// site loads a site the user may see and the plugin is on for.
func (p *Plugin) site(w http.ResponseWriter, r *http.Request) (model.Site, bool) {
	ctx := r.Context()
	u := plugin.User(r)
	site, err := p.host.Site(ctx, r.PathValue("name"))
	if err != nil || !u.IsAdmin() && site.AccountID != u.AccountID {
		plugin.WriteErr(w, 404, "no such site")
		return site, false
	}
	if !p.host.ActiveFor(ctx, site.AccountID) {
		plugin.WriteErr(w, 403, "the Cloudflare plugin is not on for this site's account")
		return site, false
	}
	return site, true
}

// SiteView is a site's DNS as the panel shows it.
type SiteView struct {
	Site      string `json:"site"`
	Domain    string `json:"domain"`
	Account   string `json:"account_id"`
	Connected bool   `json:"connected"` // a token is there for the site's account
	Managed   bool   `json:"managed"`   // records are kept by the plugin
	OnHalyard bool   `json:"on_halyard"`
	DNSOn     bool   `json:"dns_on"` // the admin turned DNS management on
	Proxied   bool   `json:"proxied"`
	SiteDNS
}

func (p *Plugin) view(ctx context.Context, c plugin.Config, s model.Site) SiteView {
	sd := p.siteDNS(ctx, s.Name)
	tok, _ := p.token(ctx, s.AccountID)
	_, managed := p.manages(ctx, c, &s)
	v := SiteView{Site: s.Name, Domain: s.Domain, Account: s.AccountID, Connected: tok != "", Managed: managed,
		OnHalyard: s.Halyard.Enabled, DNSOn: c.Bool("dns"), Proxied: c.Bool("proxied"), SiteDNS: sd}
	if sd.Proxied != nil {
		v.Proxied = *sd.Proxied
	}
	return v
}

func (p *Plugin) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := plugin.User(r)
	c, err := p.host.Config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	own := p.ownToken(ctx, u.AccountID)
	sites, _ := p.host.Sites(ctx)
	out := []SiteView{}
	for _, s := range sites {
		if (!u.IsAdmin() && s.AccountID != u.AccountID) || !p.host.ActiveFor(ctx, s.AccountID) {
			continue
		}
		out = append(out, p.view(ctx, c, s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	st, _ := p.host.Store.Settings(ctx)
	plugin.WriteJSON(w, 200, map[string]any{
		"own_token": own.Token != "", "zones": own.Zones, "server_token": own.Token == "" && p.host.Chosen(ctx, u.AccountID) && c.Str("token") != "",
		"dns_on": c.Bool("dns"), "proxied": c.Bool("proxied"), "ipv4": st.Brand.IPv4, "ipv6": st.Brand.IPv6, "sites": out,
	})
}

func (p *Plugin) connect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := plugin.User(r)
	var in struct {
		Token string `json:"token"`
	}
	if err := plugin.ReadJSON(r, &in); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	in.Token = strings.TrimSpace(in.Token)
	if in.Token == "" {
		plugin.WriteErr(w, 400, "paste an API token")
		return
	}
	cl := p.client(in.Token)
	if _, err := cl.Verify(ctx); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	zones, _ := cl.ZoneNames(ctx)
	if err := p.host.SetData(ctx, "account:"+u.AccountID, accountToken{Token: in.Token, Zones: zones, Saved: time.Now().UTC()}); err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	p.host.Event(ctx, "info", u.Username+" connected a Cloudflare account ("+strings.Join(zones, ", ")+")")
	go p.syncAccount(context.WithoutCancel(ctx), u.AccountID)
	plugin.WriteJSON(w, 200, map[string]any{"ok": true, "zones": zones})
}

func (p *Plugin) disconnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := plugin.User(r)
	if err := p.host.SetData(ctx, "account:"+u.AccountID, accountToken{}); err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	p.host.Event(ctx, "info", u.Username+" disconnected their Cloudflare account")
	plugin.WriteJSON(w, 200, map[string]any{"ok": true})
}

// syncAccount brings every site of an account (or all accounts for "") up to date.
func (p *Plugin) syncAccount(ctx context.Context, account string) int {
	c, err := p.host.Config(ctx)
	if err != nil {
		return 0
	}
	sites, _ := p.host.Sites(ctx)
	n := 0
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range sites {
		if account != "" && s.AccountID != account || s.Status != model.StatusActive {
			continue
		}
		if tok, ok := p.manages(ctx, c, &s); ok {
			p.syncSite(ctx, c, tok, s, false, func(string, ...any) {})
			n++
		}
	}
	return n
}

func (p *Plugin) syncAll(w http.ResponseWriter, r *http.Request) {
	u := plugin.User(r)
	account := u.AccountID
	if u.IsAdmin() {
		account = ""
	}
	n := p.syncAccount(r.Context(), account)
	plugin.WriteJSON(w, 200, map[string]any{"ok": true, "sites": n})
}

func (p *Plugin) getSiteDNS(w http.ResponseWriter, r *http.Request) {
	site, ok := p.site(w, r)
	if !ok {
		return
	}
	c, err := p.host.Config(r.Context())
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	plugin.WriteJSON(w, 200, p.view(r.Context(), c, site))
}

// setSiteDNS changes a site's DNS options and syncs it. adopt replaces
// records someone else made.
func (p *Plugin) setSiteDNS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	site, ok := p.site(w, r)
	if !ok {
		return
	}
	var in struct {
		Off     *bool `json:"off"`
		Proxied *bool `json:"proxied"`
		Adopt   bool  `json:"adopt"`
	}
	if err := plugin.ReadJSON(r, &in); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	c, err := p.host.Config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	p.mu.Lock()
	sd := p.siteDNS(ctx, site.Name)
	if in.Off != nil {
		sd.Off = *in.Off
	}
	if in.Proxied != nil {
		v := *in.Proxied
		sd.Proxied = &v
	}
	_ = p.host.SetData(ctx, "site:"+site.Name, sd)
	tok, managed := p.manages(ctx, c, &site)
	var err2 error
	if managed {
		p.syncSite(ctx, c, tok, site, in.Adopt, func(string, ...any) {})
		if in.Adopt {
			p.host.SiteEvent(ctx, "info", site.Name, plugin.User(r).Username+" took over Cloudflare DNS for "+site.Domain)
		}
	} else if in.Adopt {
		err2 = errors.New("DNS is not managed for this site; turn it on first")
	}
	p.mu.Unlock()
	if err2 != nil {
		plugin.WriteErr(w, 400, err2.Error())
		return
	}
	plugin.WriteJSON(w, 200, p.view(ctx, c, site))
}

func (p *Plugin) purge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	site, ok := p.site(w, r)
	if !ok {
		return
	}
	tok, _ := p.token(ctx, site.AccountID)
	if tok == "" {
		plugin.WriteErr(w, 400, "connect a Cloudflare account on the Cloudflare page first")
		return
	}
	n, err := p.client(tok).Purge(ctx, site.Hostnames())
	if err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	p.host.SiteEvent(ctx, "info", site.Name, fmt.Sprintf("%s cleared Cloudflare's cache for %s", plugin.User(r).Username, site.Domain))
	plugin.WriteJSON(w, 200, map[string]any{"ok": true, "hosts": n})
}
