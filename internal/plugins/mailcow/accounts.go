package mailcow

// Mail domains belong to Berth accounts. Admins see and manage every domain;
// hosting customers whose plan includes mail (or who own a domain) get the
// Mail page for their own domains, within their plan's limits.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
)

// Owner records which account a mail domain belongs to.
type Owner struct {
	Account string    `json:"account"`
	Created time.Time `json:"created"`
}

// owners loads the domain owners. Domains missing from it (ones made in
// mailcow directly or before accounts existed) belong to the admin's account.
func (p *Plugin) owners(ctx context.Context) map[string]Owner {
	o := map[string]Owner{}
	_ = p.host.GetData(ctx, "owners", &o)
	if o == nil {
		o = map[string]Owner{}
	}
	return o
}

// ownerOf returns the account a domain belongs to.
func (p *Plugin) ownerOf(ctx context.Context, domain string) string {
	if o, ok := p.owners(ctx)[domain]; ok {
		return o.Account
	}
	return p.host.AdminAccount(ctx)
}

func (p *Plugin) setOwner(ctx context.Context, domain, account string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := p.owners(ctx)
	if account == "" {
		delete(o, domain)
	} else {
		cur := o[domain]
		if cur.Created.IsZero() {
			cur.Created = time.Now().UTC()
		}
		cur.Account = account
		o[domain] = cur
	}
	return p.host.SetData(ctx, "owners", o)
}

// owned lists the domains an account owns explicitly.
func (p *Plugin) owned(ctx context.Context, account string) []string {
	var out []string
	for d, o := range p.owners(ctx) {
		if o.Account == account {
			out = append(out, d)
		}
	}
	return out
}

// AllowAccount implements plugin.Gate: customers get the Mail page when
// their plan includes mail or they already own a mail domain.
func (p *Plugin) AllowAccount(ctx context.Context, account string) bool {
	if pl, ok := p.host.Plan(ctx, account); ok && pl.MailDomains > 0 {
		return true
	}
	return len(p.owned(ctx, account)) > 0
}

// customer returns the signed-in user's account when they are not an admin.
func customer(r *http.Request) (string, bool) {
	u := plugin.User(r)
	if u.IsAdmin() {
		return "", false
	}
	return u.AccountID, true
}

// mayUse reports whether the signed-in user may manage a domain, writing a
// 404 when not. Admins may manage every domain.
func (p *Plugin) mayUse(w http.ResponseWriter, r *http.Request, domain string) bool {
	acc, isCustomer := customer(r)
	if !isCustomer {
		return true
	}
	if o, ok := p.owners(r.Context())[domain]; ok && o.Account == acc {
		return true
	}
	plugin.WriteErr(w, 404, "no such mail domain")
	return false
}

// limits are the mail limits of an account's plan.
type limits struct {
	Domains   int `json:"domains"`    // 0 = no mail (customers)
	Mailboxes int `json:"mailboxes"`  // 0 = unlimited
	Aliases   int `json:"aliases"`    // 0 = unlimited
	StorageMB int `json:"storage_mb"` // 0 = the plugin's per-domain default
}

func (p *Plugin) limitsOf(ctx context.Context, account string) (limits, bool) {
	pl, ok := p.host.Plan(ctx, account)
	if !ok {
		return limits{}, false
	}
	return limits{Domains: pl.MailDomains, Mailboxes: pl.Mailboxes, Aliases: pl.MailAliases, StorageMB: pl.MailStorageMB}, true
}

// usage is what an account's mail domains hold.
type usage struct {
	Domains   int   `json:"domains"`
	Mailboxes int64 `json:"mailboxes"`
	Aliases   int64 `json:"aliases"`
	QuotaMB   int64 `json:"quota_mb"` // storage given to its domains
	BytesUsed int64 `json:"bytes_used"`
}

func usageOf(ds []Domain) usage {
	var u usage
	for _, d := range ds {
		u.Domains++
		u.Mailboxes += d.Mailboxes
		u.Aliases += d.Aliases
		u.QuotaMB += d.QuotaMB
		u.BytesUsed += d.BytesUsed
	}
	return u
}

// accountDomains loads the mailcow domains an account owns.
func (p *Plugin) accountDomains(ctx context.Context, mc *Client, account string) ([]Domain, error) {
	mine := map[string]bool{}
	for _, d := range p.owned(ctx, account) {
		mine[d] = true
	}
	all, err := mc.Domains(ctx)
	if err != nil {
		return nil, err
	}
	var out []Domain
	for _, d := range all {
		if mine[d.Name] {
			out = append(out, d)
		}
	}
	return out, nil
}

// newDomainFor works out a new domain's mailcow limits for an account: its
// plan's mailbox and alias limits, and whatever storage the plan has left.
func (p *Plugin) newDomainFor(ctx context.Context, c plugin.Config, mc *Client, account, domain string) (NewDomain, error) {
	nd := NewDomain{Name: domain, MaxMailboxes: c.Int("max_mailboxes"), MaxAliases: c.Int("max_aliases"),
		QuotaMB: c.Int("domain_quota_mb"), DefQuotaMB: c.Int("mailbox_quota_mb")}
	if l, ok := p.limitsOf(ctx, account); ok {
		if l.Mailboxes > 0 {
			nd.MaxMailboxes = l.Mailboxes
		}
		if l.Aliases > 0 {
			nd.MaxAliases = l.Aliases
		}
		if l.StorageMB > 0 {
			have, err := p.accountDomains(ctx, mc, account)
			if err != nil {
				return nd, err
			}
			left := int64(l.StorageMB) - usageOf(have).QuotaMB
			if left < 100 {
				return nd, &plugin.Error{Code: 409, Msg: "the plan's mail storage is all given to other domains"}
			}
			nd.QuotaMB = int(left)
		}
	}
	nd.MaxQuotaMB = nd.QuotaMB
	if nd.DefQuotaMB > nd.QuotaMB {
		nd.DefQuotaMB = nd.QuotaMB
	}
	return nd, nil
}

// verifyName and verifyValue are the TXT record a customer adds to prove a
// domain is theirs before adding it as a mail domain.
func verifyName(domain string) string { return "_berth-verify." + domain }

func verifyValue(account, domain string) string {
	h := sha256.Sum256([]byte("berth-mail:" + account + ":" + domain))
	return "berth-verify=" + hex.EncodeToString(h[:12])
}

// proven reports whether a customer controls a domain: it is one of their
// zones in the DNS plugin, or it has their verification TXT record.
func (p *Plugin) proven(ctx context.Context, account, domain string) bool {
	if z, ok := p.zones(ctx); ok && z.ZoneOf(ctx, account, domain) != "" {
		return true
	}
	var res DNS = Resolver{}
	if p.dns != nil {
		res = p.dns
	}
	got, _ := res.Lookup(ctx, "TXT", verifyName(domain))
	want := verifyValue(account, domain)
	for _, g := range got {
		if normTXT(g) == want {
			return true
		}
	}
	return false
}

// zoneHost is what the DNS plugin offers other plugins.
type zoneHost interface {
	ZoneOf(ctx context.Context, account, host string) string
	Records(ctx context.Context, zone, typ, name string) ([]string, error)
	AddRecord(ctx context.Context, zone, typ, name, content string) error
}

func (p *Plugin) zones(ctx context.Context) (zoneHost, bool) {
	if p.zoneHost != nil {
		return p.zoneHost, true
	}
	if p.host == nil {
		return nil, false
	}
	other, ok := p.host.Plugin(ctx, "dns")
	if !ok {
		return nil, false
	}
	z, ok := other.(zoneHost)
	return z, ok
}

// berthDNS reads and writes a zone on Berth's own nameservers.
type berthDNS struct {
	z    zoneHost
	zone string
}

func (b berthDNS) Lookup(ctx context.Context, typ, name string) ([]string, error) {
	return b.z.Records(ctx, b.zone, typ, name)
}

func (b berthDNS) Create(ctx context.Context, r Record) error {
	content := r.Content
	switch r.Type {
	case "MX":
		content = fmt.Sprintf("%d %s.", r.Priority, normHost(r.Content))
	case "CNAME":
		content = normHost(r.Content) + "."
	case "TXT":
		content = quoteTXT(r.Content)
	}
	return b.z.AddRecord(ctx, b.zone, r.Type, r.Name, content)
}

// dnsForDomain picks where a domain's records live: a zone on Berth's own
// nameservers owned by the domain's account, the Cloudflare account of the
// plugin's token, or (neither) plain lookups with the records shown to add.
func (p *Plugin) dnsForDomain(ctx context.Context, c plugin.Config, owner, domain string) (DNS, string) {
	if p.dns != nil {
		return p.dns, "test"
	}
	if z, ok := p.zones(ctx); ok && owner != "" {
		if zone := z.ZoneOf(ctx, owner, domain); zone != "" {
			return berthDNS{z: z, zone: zone}, "berth"
		}
	}
	if t := c.Str("cf_token"); t != "" {
		cf := &Cloudflare{Token: t, Base: p.cfBase, HTTP: p.httpClient}
		if _, err := cf.zone(ctx, domain); err == nil {
			return cf, "cloudflare"
		}
	}
	return Resolver{}, "lookup"
}

// provisionReq is what a billing system sends to set mail up for an account.
type provisionReq struct {
	Domain string `json:"domain"`
}

// Provision implements plugin.Provisioner: it adds a mail domain for an
// account (or finishes setting up one the account already has).
func (p *Plugin) Provision(ctx context.Context, a model.Account, body json.RawMessage) (any, error) {
	var req provisionReq
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, &plugin.Error{Code: 400, Msg: "invalid JSON: " + err.Error()}
		}
	}
	d, err := cleanDomain(req.Domain)
	if err != nil {
		return nil, &plugin.Error{Code: 400, Msg: err.Error()}
	}
	c, err := p.host.Config(ctx)
	if err != nil {
		return nil, err
	}
	mc := p.mailcow(c)
	if o, ok := p.owners(ctx)[d]; ok {
		if o.Account != a.ID {
			return nil, &plugin.Error{Code: 409, Msg: d + " is a mail domain of another account"}
		}
		res := p.Setup(ctx, c, d, a.ID, c.Bool("ses_default"))
		return map[string]any{"domain": d, "created": false, "setup": res}, nil
	}
	if _, err := mc.Domain(ctx, d); err == nil {
		return nil, &plugin.Error{Code: 409, Msg: d + " is already a mail domain in mailcow"}
	} else if !errors.Is(err, ErrNoDomain) {
		return nil, err
	}
	nd, err := p.newDomainFor(ctx, c, mc, a.ID, d)
	if err != nil {
		return nil, err
	}
	nd.Description = a.Name
	if err := mc.AddDomain(ctx, nd); err != nil {
		return nil, err
	}
	if err := p.setOwner(ctx, d, a.ID); err != nil {
		return nil, err
	}
	if a.Suspended {
		_ = mc.EditDomain(ctx, d, map[string]any{"active": "0"})
	}
	res := p.Setup(ctx, c, d, a.ID, c.Bool("ses_default"))
	p.host.Event(ctx, "info", "Added mail domain "+d+" for "+a.Name)
	return map[string]any{"domain": d, "created": true, "setup": res, "webmail": webmail(c)}, nil
}

// webmail is where mailbox users read their mail.
func webmail(c plugin.Config) string {
	if u := c.Str("webmail_url"); u != "" {
		return u
	}
	if h := mailHost(c); h != "" {
		return "https://" + h + "/SOGo/"
	}
	return ""
}

// AccountChanged implements plugin.AccountWatcher: a suspended account's mail
// domains stop working until it is unsuspended, and a deleted account's
// domains, mailboxes and mail are removed.
func (p *Plugin) AccountChanged(ctx context.Context, a model.Account, event string) {
	domains := p.owned(ctx, a.ID)
	if len(domains) == 0 {
		return
	}
	c, err := p.host.Config(ctx)
	if err != nil {
		return
	}
	mc := p.mailcow(c)
	for _, d := range domains {
		var err error
		switch event {
		case plugin.AccountSuspended, plugin.AccountUnsuspended:
			on := map[bool]string{true: "0", false: "1"}[event == plugin.AccountSuspended]
			err = mc.EditDomain(ctx, d, map[string]any{"active": on})
			if err == nil {
				p.host.Event(ctx, "info", fmt.Sprintf("Mail domain %s %s with account %s", d, event, a.Name))
			}
		case plugin.AccountDeleted:
			err = p.removeDomain(ctx, c, mc, d)
			if err == nil {
				p.host.Event(ctx, "warn", "Deleted mail domain "+d+", its mailboxes and mail, with account "+a.Name)
			}
		}
		if err != nil {
			p.host.Event(ctx, "error", fmt.Sprintf("Mail domain %s: following account %s (%s) failed: %v", d, a.Name, event, err))
		}
	}
}

// removeDomain deletes a domain with its mailboxes, aliases and SES identity.
func (p *Plugin) removeDomain(ctx context.Context, c plugin.Config, mc *Client, d string) error {
	if _, err := mc.Domain(ctx, d); errors.Is(err, ErrNoDomain) {
		return p.setOwner(ctx, d, "")
	}
	mbs, err := mc.Mailboxes(ctx, d)
	if err != nil {
		return err
	}
	for _, m := range mbs {
		if err := mc.DeleteMailbox(ctx, m.Address); err != nil {
			return err
		}
	}
	als, err := mc.Aliases(ctx, d)
	if err != nil {
		return err
	}
	for _, a := range als {
		if err := mc.DeleteAlias(ctx, a.ID); err != nil {
			return err
		}
	}
	if err := mc.DeleteDomain(ctx, d); err != nil {
		return err
	}
	p.setPending(ctx, d, false)
	if s := p.ses(c); s != nil {
		_ = s.DeleteIdentity(ctx, d)
	}
	return p.setOwner(ctx, d, "")
}

// checkLimit returns a 409 error when n has reached limit (0 = unlimited).
func checkLimit(n int64, limit int, what string) error {
	if limit > 0 && n >= int64(limit) {
		return &plugin.Error{Code: 409, Msg: fmt.Sprintf("your plan allows %d %s; remove one or upgrade first", limit, what)}
	}
	return nil
}

// writePluginErr writes a plugin.Error with its status, other errors as 502.
func writePluginErr(w http.ResponseWriter, err error) {
	var pe *plugin.Error
	if errors.As(err, &pe) {
		plugin.WriteErr(w, pe.Code, pe.Msg)
		return
	}
	fail(w, err)
}

// domainOf returns the domain of an email address.
func domainOf(addr string) string {
	_, d, _ := strings.Cut(addr, "@")
	return normHost(d)
}
