// Package mailcow is the Berth plugin that manages mailcow mail domains,
// mailboxes and aliases, and sends each domain's outgoing mail through Amazon
// SES (domain identity with Easy DKIM, custom MAIL FROM and a mailcow
// sender-dependent transport).
package mailcow

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/plugin"
)

// Plugin is the mailcow plugin.
type Plugin struct {
	host *plugin.Host
	mu   sync.Mutex // serialises changes to the pending list and relayhost

	// Test hooks.
	httpClient *http.Client
	sesBase    string
	cfBase     string
	dns        DNS
}

// New returns the plugin.
func New() *Plugin { return &Plugin{} }

const sec = "Mailcow"

// Info implements plugin.Plugin.
func (p *Plugin) Info() plugin.Info {
	return plugin.Info{
		ID:          "mailcow",
		Name:        "Mailcow",
		Description: "Manage mailcow mail domains, mailboxes and aliases. New domains are verified in Amazon SES and send their mail through it, with the DNS records created in Cloudflare.",
		Version:     "1.0",
		Icon:        "mail",
		NavLabel:    "Mail",
		AdminOnly:   true,
		Fields: []plugin.Field{
			{Key: "url", Label: "mailcow address", Type: plugin.Text, Section: sec, Required: true, Placeholder: "https://mail.example.com", Help: "The address of your mailcow web UI."},
			{Key: "api_key", Label: "API key", Type: plugin.Secret, Section: sec, Required: true, Help: "mailcow → System → Configuration → Access → API: a read-write key. Add this server's IP to the allowed networks."},
			{Key: "insecure", Label: "Skip certificate check", Type: plugin.Bool, Section: sec, Help: "Only for a mailcow with a self-signed certificate."},
			{Key: "mail_host", Label: "Mail server hostname", Type: plugin.Text, Section: sec, Placeholder: "mail.example.com", Help: "What MX and autodiscover records point at. Blank uses the host of the mailcow address."},
			{Key: "max_mailboxes", Label: "Mailboxes per new domain", Type: plugin.Number, Section: "New domains", Default: 25},
			{Key: "max_aliases", Label: "Aliases per new domain", Type: plugin.Number, Section: "New domains", Default: 100},
			{Key: "domain_quota_mb", Label: "Domain storage (MB)", Type: plugin.Number, Section: "New domains", Default: 10240},
			{Key: "mailbox_quota_mb", Label: "Mailbox storage (MB)", Type: plugin.Number, Section: "New domains", Default: 3072},

			{Key: "ses_region", Label: "Region", Type: plugin.Text, Section: "Amazon SES", Default: "eu-west-2", Placeholder: "eu-west-2"},
			{Key: "ses_access_key", Label: "Access key ID", Type: plugin.Text, Section: "Amazon SES", Help: "An IAM user allowed ses:* identity actions, ses:GetAccount and ses:SendRawEmail. Its secret also gives mailcow the SES SMTP password."},
			{Key: "ses_secret_key", Label: "Secret access key", Type: plugin.Secret, Section: "Amazon SES"},
			{Key: "ses_default", Label: "Send new domains' mail through SES", Type: plugin.Bool, Section: "Amazon SES", Default: true, Help: "Adds the domain to SES and turns on the SES sender-dependent transport for it once SES has verified the domain."},
			{Key: "ses_mail_from", Label: "Custom MAIL FROM subdomain", Type: plugin.Text, Section: "Amazon SES", Default: "bounce", Help: "Bounces go to bounce.<domain> so SPF aligns for DMARC. Blank uses amazonses.com."},
			{Key: "smtp_user", Label: "SES SMTP username (optional)", Type: plugin.Text, Section: "Amazon SES", Help: "Only if mailcow should relay with separate SMTP credentials instead of the access key above."},
			{Key: "smtp_password", Label: "SES SMTP password (optional)", Type: plugin.Secret, Section: "Amazon SES"},

			{Key: "cf_token", Label: "Cloudflare API token", Type: plugin.Secret, Section: "DNS", Help: "Zone → DNS → Edit for your mail domains. Without one, Berth shows the records for you to add yourself."},
			{Key: "dns_create", Label: "Create missing DNS records", Type: plugin.Bool, Section: "DNS", Default: true, Help: "Existing records that differ are never changed; they are shown as conflicts."},
			{Key: "dmarc", Label: "DMARC record for new domains", Type: plugin.Text, Section: "DNS", Default: "v=DMARC1; p=none", Help: "Added only when the domain has no DMARC record. Blank skips it."},
		},
	}
}

var regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)

// Validate implements plugin.Validator.
func (p *Plugin) Validate(c plugin.Config) error {
	u, err := url.Parse(c.Str("url"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("mailcow address must look like https://mail.example.com")
	}
	if r := c.Str("ses_region"); c.Str("ses_access_key") != "" && !regionRe.MatchString(r) {
		return fmt.Errorf("%q is not an AWS region (e.g. eu-west-2)", r)
	}
	if mf := c.Str("ses_mail_from"); mf != "" && !labelRe.MatchString(mf) {
		return errors.New("the MAIL FROM subdomain must be a single label like bounce")
	}
	return nil
}

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Test implements plugin.Tester.
func (p *Plugin) Test(ctx context.Context, c plugin.Config) (string, error) {
	if err := p.Validate(c); err != nil {
		return "", err
	}
	v, err := p.mailcow(c).Version(ctx)
	if err != nil {
		return "", err
	}
	msg := []string{"mailcow " + v + " answered"}
	if s := p.ses(c); s != nil {
		a, err := s.Account(ctx)
		if err != nil {
			return "", err
		}
		switch {
		case !a.SendingEnabled:
			msg = append(msg, "SES sending is paused on this account")
		case !a.ProductionAccessEnabled:
			msg = append(msg, "SES works but the account is in the sandbox (it only sends to verified addresses until AWS grants production access)")
		default:
			msg = append(msg, fmt.Sprintf("SES works (%.0f emails a day)", a.SendQuota.Max24HourSend))
		}
	}
	if t := c.Str("cf_token"); t != "" {
		if err := (&Cloudflare{Token: t, Base: p.cfBase, HTTP: p.httpClient}).Verify(ctx); err != nil {
			return "", err
		}
		msg = append(msg, "Cloudflare token works")
	}
	return strings.Join(msg, ". ") + ".", nil
}

func (p *Plugin) mailcow(c plugin.Config) *Client {
	return &Client{URL: c.Str("url"), Key: c.Str("api_key"), Insecure: c.Bool("insecure"), HTTP: p.httpClient}
}

// ses returns an SES client, or nil when SES is not set up.
func (p *Plugin) ses(c plugin.Config) *SES {
	if c.Str("ses_access_key") == "" || c.Str("ses_secret_key") == "" || c.Str("ses_region") == "" {
		return nil
	}
	return &SES{Region: c.Str("ses_region"), AccessKey: c.Str("ses_access_key"), SecretKey: c.Str("ses_secret_key"), Endpoint: p.sesBase, HTTP: p.httpClient}
}

func (p *Plugin) dnsFor(c plugin.Config) (DNS, string) {
	if p.dns != nil {
		return p.dns, "test"
	}
	if t := c.Str("cf_token"); t != "" {
		return &Cloudflare{Token: t, Base: p.cfBase, HTTP: p.httpClient}, "cloudflare"
	}
	return Resolver{}, "lookup"
}

func mailHost(c plugin.Config) string {
	if h := c.Str("mail_host"); h != "" {
		return normHost(h)
	}
	u, err := url.Parse(c.Str("url"))
	if err != nil {
		return ""
	}
	return normHost(u.Hostname())
}

func smtpCreds(c plugin.Config) (string, string) {
	if u := c.Str("smtp_user"); u != "" {
		return u, c.Str("smtp_password")
	}
	return c.Str("ses_access_key"), SMTPPassword(c.Str("ses_secret_key"), c.Str("ses_region"))
}

// Step is one part of setting up a domain.
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, error, skip
	Detail string `json:"detail,omitempty"`
}

// SetupResult is what setting up a domain did.
type SetupResult struct {
	Steps   []Step   `json:"steps"`
	Records []Record `json:"records"`
}

func (r *SetupResult) add(name, status, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, Status: status, Detail: detail})
}

// ensureRelayhost finds or creates mailcow's SES transport and returns its ID.
func (p *Plugin) ensureRelayhost(ctx context.Context, c plugin.Config, mc *Client) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	host := SMTPHost(c.Str("ses_region"))
	user, pass := smtpCreds(c)
	if user == "" || pass == "" {
		return "", errors.New("no SES SMTP credentials")
	}
	find := func() (Relayhost, bool, error) {
		list, err := mc.Relayhosts(ctx)
		if err != nil {
			return Relayhost{}, false, err
		}
		for _, r := range list {
			if strings.EqualFold(r.Hostname, host) && r.Username == user {
				return r, true, nil
			}
		}
		return Relayhost{}, false, nil
	}
	r, ok, err := find()
	if err != nil {
		return "", err
	}
	if ok {
		attr := map[string]any{}
		if r.Password != "" && r.Password != pass {
			attr["password"] = pass
		}
		if !r.Active {
			attr["active"] = "1"
		}
		if len(attr) > 0 {
			if err := mc.EditRelayhost(ctx, r.ID, attr); err != nil {
				return "", err
			}
		}
		return r.ID, nil
	}
	if err := mc.AddRelayhost(ctx, host, user, pass); err != nil {
		return "", err
	}
	r, ok, err = find()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("mailcow did not list the SES transport after adding it")
	}
	return r.ID, nil
}

// pending holds domains waiting for SES verification before their transport
// is switched on, with the time they were added.
type pendingList map[string]time.Time

func (p *Plugin) pending(ctx context.Context) pendingList {
	l := pendingList{}
	_ = p.host.GetData(ctx, "pending", &l)
	return l
}

func (p *Plugin) setPending(ctx context.Context, domain string, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.pending(ctx)
	if on {
		if _, ok := l[domain]; ok {
			return
		}
		l[domain] = time.Now().UTC()
	} else {
		if _, ok := l[domain]; !ok {
			return
		}
		delete(l, domain)
	}
	_ = p.host.SetData(ctx, "pending", l)
}

// Setup makes sure a mailcow domain has its DKIM key, SES identity, MAIL FROM,
// SES transport (when wanted) and DNS records. It is safe to run again.
func (p *Plugin) Setup(ctx context.Context, c plugin.Config, domain string, useSES bool) SetupResult {
	var res SetupResult
	mc := p.mailcow(c)

	dkim, err := mc.DKIM(ctx, domain)
	if err == nil && dkim.TXT == "" {
		if err = mc.AddDKIM(ctx, domain, "dkim"); err == nil {
			dkim, err = mc.DKIM(ctx, domain)
		}
		if err == nil {
			res.add("mailcow DKIM key", "ok", "Created a 2048-bit key (selector "+dkim.Selector+")")
		}
	} else if err == nil {
		res.add("mailcow DKIM key", "ok", "Already there (selector "+dkim.Selector+")")
	}
	if err != nil {
		res.add("mailcow DKIM key", "error", err.Error())
	}

	plan := Plan{Domain: domain, MailHost: mailHost(c), DKIM: dkim, DMARC: c.Str("dmarc")}
	s := p.ses(c)
	switch {
	case !useSES:
		res.add("Amazon SES", "skip", "Not used for this domain")
	case s == nil:
		res.add("Amazon SES", "skip", "Add SES keys on the Plugins page to send through SES")
	default:
		id, created, err := s.EnsureIdentity(ctx, domain)
		if err != nil {
			res.add("Amazon SES identity", "error", err.Error())
			break
		}
		plan.SES, plan.Tokens, plan.Region = true, id.Dkim.Tokens, s.Region
		if created {
			res.add("Amazon SES identity", "ok", "Added the domain with Easy DKIM")
		} else {
			res.add("Amazon SES identity", "ok", "Already in SES ("+strings.ToLower(id.Dkim.Status)+")")
		}
		if mf := c.Str("ses_mail_from"); mf != "" {
			plan.MailFrom = mf
			want := mf + "." + domain
			if !strings.EqualFold(id.MailFrom.Domain, want) {
				if err := s.SetMailFrom(ctx, domain, want); err != nil {
					res.add("SES MAIL FROM", "error", err.Error())
				} else {
					res.add("SES MAIL FROM", "ok", "Bounces go to "+want)
				}
			}
		}
		if id.VerifiedForSending {
			if err := p.transport(ctx, c, mc, domain, true); err != nil {
				res.add("SES transport", "error", err.Error())
			} else {
				res.add("SES transport", "ok", "mailcow sends this domain's mail through "+SMTPHost(s.Region))
			}
		} else {
			p.setPending(ctx, domain, true)
			res.add("SES transport", "warn", "Turns on by itself once SES has verified the domain (usually minutes after the DNS records exist). Until then mail goes out directly from mailcow.")
		}
	}

	d, mode := p.dnsFor(c)
	create := mode != "lookup" && c.Bool("dns_create")
	res.Records = Check(ctx, d, plan.Records(), create)
	var made, missing, clash, failed int
	for _, r := range res.Records {
		switch r.Status {
		case "created":
			made++
		case "missing":
			missing++
		case "conflict":
			clash++
		case "error":
			failed++
		}
	}
	switch {
	case failed > 0:
		res.add("DNS records", "error", fmt.Sprintf("%d could not be checked or created; see the list", failed))
	case clash > 0:
		res.add("DNS records", "warn", fmt.Sprintf("%d clash with records that are already there; fix those by hand", clash))
	case missing > 0 && mode == "lookup":
		res.add("DNS records", "warn", fmt.Sprintf("Add the %d missing records shown below at your DNS provider (or add a Cloudflare token)", missing))
	case missing > 0:
		res.add("DNS records", "warn", fmt.Sprintf("%d missing (record creation is off)", missing))
	case made > 0:
		res.add("DNS records", "ok", fmt.Sprintf("Created %d in Cloudflare", made))
	default:
		res.add("DNS records", "ok", "All in place")
	}
	return res
}

// transport turns the SES sender-dependent transport on or off for a domain.
func (p *Plugin) transport(ctx context.Context, c plugin.Config, mc *Client, domain string, on bool) error {
	id := "0"
	if on {
		var err error
		if id, err = p.ensureRelayhost(ctx, c, mc); err != nil {
			return err
		}
	}
	if err := mc.EditDomain(ctx, domain, map[string]any{"relayhost": id}); err != nil {
		return err
	}
	p.setPending(ctx, domain, false)
	return nil
}

// watch turns on the SES transport for domains once SES verifies them.
func (p *Plugin) watch() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		p.checkPending(context.Background())
	}
}

func (p *Plugin) checkPending(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if !p.host.Enabled(ctx) {
		return
	}
	l := p.pending(ctx)
	if len(l) == 0 {
		return
	}
	c, err := p.host.Config(ctx)
	if err != nil {
		return
	}
	s := p.ses(c)
	if s == nil {
		return
	}
	mc := p.mailcow(c)
	for domain, since := range l {
		id, err := s.Identity(ctx, domain)
		switch {
		case isSESType(err, "NotFoundException"):
			p.setPending(ctx, domain, false)
		case err != nil:
			p.host.Log.Warn("SES check failed", "domain", domain, "err", err)
		case id.VerifiedForSending:
			if err := p.transport(ctx, c, mc, domain, true); err != nil {
				p.host.Event(ctx, "error", domain+": SES verified it but turning on the SES transport failed: "+err.Error())
				continue
			}
			p.host.Event(ctx, "info", domain+": verified by Amazon SES; mail now goes out through SES")
		case time.Since(since) > 7*24*time.Hour:
			p.setPending(ctx, domain, false)
			p.host.Event(ctx, "warn", domain+": Amazon SES did not verify the domain within a week; stopped waiting. Check its DNS records on the Mail page.")
		}
	}
}

// Init implements plugin.Plugin.
func (p *Plugin) Init(h *plugin.Host) http.Handler {
	p.host = h
	go p.watch()
	m := http.NewServeMux()
	m.HandleFunc("GET /overview", p.overview)
	m.HandleFunc("POST /domains", p.addDomain)
	m.HandleFunc("GET /domains/{domain}", p.getDomain)
	m.HandleFunc("PUT /domains/{domain}", p.editDomain)
	m.HandleFunc("DELETE /domains/{domain}", p.deleteDomain)
	m.HandleFunc("POST /domains/{domain}/setup", p.setupDomain)
	m.HandleFunc("POST /domains/{domain}/mailboxes", p.addMailbox)
	m.HandleFunc("PUT /mailboxes/{address}", p.editMailbox)
	m.HandleFunc("DELETE /mailboxes/{address}", p.deleteMailbox)
	m.HandleFunc("POST /domains/{domain}/aliases", p.addAlias)
	m.HandleFunc("DELETE /domains/{domain}/aliases/{id}", p.deleteAlias)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { plugin.WriteErr(w, 404, "no such endpoint") })
	return m
}

func (p *Plugin) config(w http.ResponseWriter, r *http.Request) (plugin.Config, bool) {
	c, err := p.host.Config(r.Context())
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return nil, false
	}
	return c, true
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func cleanDomain(s string) (string, error) {
	d := normHost(s)
	if !domainRe.MatchString(d) {
		return "", fmt.Errorf("%q is not a domain name", s)
	}
	return d, nil
}

func fail(w http.ResponseWriter, err error) {
	code := 502
	if errors.Is(err, ErrNoDomain) {
		code = 404
	}
	plugin.WriteErr(w, code, err.Error())
}

// isSESRelay reports whether a relayhost is Amazon SES.
func isSESRelay(h string) bool { return strings.Contains(strings.ToLower(h), ".amazonaws.com") }

func (p *Plugin) decorate(ctx context.Context, mc *Client, ds []Domain) {
	rh, _ := mc.Relayhosts(ctx)
	byID := map[string]string{}
	for _, r := range rh {
		byID[r.ID] = r.Hostname
	}
	pend := p.pending(ctx)
	sites, _ := p.host.Sites(ctx)
	for i := range ds {
		d := &ds[i]
		d.TransportHost = byID[d.RelayhostID]
		d.SESTransport = isSESRelay(d.TransportHost)
		_, d.Pending = pend[d.Name]
		for _, s := range sites {
			if strings.EqualFold(strings.TrimPrefix(s.Domain, "www."), d.Name) {
				d.Site = s.ID
			}
		}
	}
}

func (p *Plugin) overview(w http.ResponseWriter, r *http.Request) {
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	mc := p.mailcow(c)
	_, mode := p.dnsFor(c)
	out := map[string]any{"domains": []Domain{}, "suggestions": []string{}, "ses": p.ses(c) != nil, "ses_default": c.Bool("ses_default"),
		"dns_mode": mode, "mail_host": mailHost(c), "url": c.Str("url"), "mailbox_quota_mb": c.Int("mailbox_quota_mb")}
	ds, err := mc.Domains(ctx)
	if err != nil {
		out["error"] = err.Error()
		plugin.WriteJSON(w, 200, out)
		return
	}
	p.decorate(ctx, mc, ds)
	sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	out["domains"] = ds
	have := map[string]bool{}
	for _, d := range ds {
		have[d.Name] = true
	}
	sites, _ := p.host.Sites(ctx)
	var sug []string
	for _, s := range sites {
		d := strings.TrimPrefix(strings.ToLower(s.Domain), "www.")
		if !have[d] && domainRe.MatchString(d) {
			have[d] = true
			sug = append(sug, d)
		}
	}
	sort.Strings(sug)
	if sug != nil {
		out["suggestions"] = sug
	}
	plugin.WriteJSON(w, 200, out)
}

func (p *Plugin) addDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain       string `json:"domain"`
		Description  string `json:"description"`
		SES          *bool  `json:"ses"`
		MaxMailboxes int    `json:"max_mailboxes"`
		QuotaMB      int    `json:"quota_mb"`
	}
	if err := plugin.ReadJSON(r, &req); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	d, err := cleanDomain(req.Domain)
	if err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	mc := p.mailcow(c)
	nd := NewDomain{Name: d, Description: req.Description, MaxMailboxes: c.Int("max_mailboxes"), MaxAliases: c.Int("max_aliases"),
		QuotaMB: c.Int("domain_quota_mb"), DefQuotaMB: c.Int("mailbox_quota_mb")}
	if req.MaxMailboxes > 0 {
		nd.MaxMailboxes = req.MaxMailboxes
	}
	if req.QuotaMB > 0 {
		nd.QuotaMB = req.QuotaMB
	}
	nd.MaxQuotaMB = nd.QuotaMB
	if nd.DefQuotaMB > nd.QuotaMB {
		nd.DefQuotaMB = nd.QuotaMB
	}
	if _, err := mc.Domain(ctx, d); err == nil {
		plugin.WriteErr(w, 409, d+" is already a mail domain in mailcow")
		return
	}
	if err := mc.AddDomain(ctx, nd); err != nil {
		fail(w, err)
		return
	}
	useSES := c.Bool("ses_default")
	if req.SES != nil {
		useSES = *req.SES
	}
	res := p.Setup(ctx, c, d, useSES)
	res.Steps = append([]Step{{Name: "mailcow domain", Status: "ok", Detail: "Added " + d}}, res.Steps...)
	p.host.Event(ctx, "info", "Added mail domain "+d)
	plugin.WriteJSON(w, 200, res)
}

func (p *Plugin) setupDomain(w http.ResponseWriter, r *http.Request) {
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d := normHost(r.PathValue("domain"))
	mc := p.mailcow(c)
	dom, err := mc.Domain(ctx, d)
	if err != nil {
		fail(w, err)
		return
	}
	ds := []Domain{dom}
	p.decorate(ctx, mc, ds)
	useSES := ds[0].SESTransport || ds[0].Pending
	var req struct {
		SES *bool `json:"ses"`
	}
	_ = plugin.ReadJSON(r, &req)
	if req.SES != nil {
		useSES = *req.SES
	}
	plugin.WriteJSON(w, 200, p.Setup(ctx, c, d, useSES))
}

func (p *Plugin) getDomain(w http.ResponseWriter, r *http.Request) {
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d := normHost(r.PathValue("domain"))
	mc := p.mailcow(c)
	dom, err := mc.Domain(ctx, d)
	if err != nil {
		fail(w, err)
		return
	}
	ds := []Domain{dom}
	p.decorate(ctx, mc, ds)
	dom = ds[0]
	out := map[string]any{"domain": dom}
	var wg sync.WaitGroup
	var mu sync.Mutex
	set := func(k string, v any) { mu.Lock(); out[k] = v; mu.Unlock() }
	wg.Add(3)
	go func() {
		defer wg.Done()
		mb, err := mc.Mailboxes(ctx, d)
		if err != nil {
			set("mailbox_error", err.Error())
		}
		if mb == nil {
			mb = []Mailbox{}
		}
		set("mailboxes", mb)
	}()
	go func() {
		defer wg.Done()
		al, err := mc.Aliases(ctx, d)
		if err != nil {
			set("alias_error", err.Error())
		}
		if al == nil {
			al = []Alias{}
		}
		set("aliases", al)
	}()
	go func() {
		defer wg.Done()
		dkim, _ := mc.DKIM(ctx, d)
		plan := Plan{Domain: d, MailHost: mailHost(c), DKIM: dkim, DMARC: c.Str("dmarc")}
		sesView := map[string]any{"configured": false}
		if s := p.ses(c); s != nil {
			sesView["configured"] = true
			id, err := s.Identity(ctx, d)
			switch {
			case isSESType(err, "NotFoundException"):
				sesView["identity"] = false
			case err != nil:
				sesView["error"] = err.Error()
			default:
				sesView["identity"] = true
				sesView["verified"] = id.VerifiedForSending
				sesView["dkim_status"] = id.Dkim.Status
				sesView["mail_from"] = id.MailFrom.Domain
				sesView["mail_from_status"] = id.MailFrom.Status
				plan.SES, plan.Tokens, plan.Region = true, id.Dkim.Tokens, s.Region
				if id.MailFrom.Domain != "" {
					plan.MailFrom = strings.TrimSuffix(id.MailFrom.Domain, "."+d)
				}
			}
		}
		set("ses", sesView)
		dn, mode := p.dnsFor(c)
		set("dns_mode", mode)
		set("records", Check(ctx, dn, plan.Records(), false))
	}()
	wg.Wait()
	plugin.WriteJSON(w, 200, out)
}

func (p *Plugin) editDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Active       *bool   `json:"active"`
		Description  *string `json:"description"`
		SESTransport *bool   `json:"ses_transport"`
	}
	if err := plugin.ReadJSON(r, &req); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d := normHost(r.PathValue("domain"))
	mc := p.mailcow(c)
	if _, err := mc.Domain(ctx, d); err != nil {
		fail(w, err)
		return
	}
	attr := map[string]any{}
	if req.Active != nil {
		attr["active"] = map[bool]string{true: "1", false: "0"}[*req.Active]
	}
	if req.Description != nil {
		attr["description"] = *req.Description
	}
	if len(attr) > 0 {
		if err := mc.EditDomain(ctx, d, attr); err != nil {
			fail(w, err)
			return
		}
	}
	msg := ""
	if req.SESTransport != nil {
		if !*req.SESTransport {
			if err := p.transport(ctx, c, mc, d, false); err != nil {
				fail(w, err)
				return
			}
			msg = "Mail for " + d + " now goes out directly from mailcow"
		} else {
			s := p.ses(c)
			if s == nil {
				plugin.WriteErr(w, 400, "add Amazon SES keys on the Plugins page first")
				return
			}
			res := p.Setup(ctx, c, d, true)
			id, err := s.Identity(ctx, d)
			if err == nil && id.VerifiedForSending {
				msg = "Mail for " + d + " now goes out through Amazon SES"
			} else {
				msg = "SES is verifying " + d + "; the SES transport turns on by itself once it is verified"
			}
			p.host.Event(ctx, "info", d+": "+msg)
			plugin.WriteJSON(w, 200, map[string]any{"message": msg, "setup": res})
			return
		}
		p.host.Event(ctx, "info", d+": "+msg)
	}
	plugin.WriteJSON(w, 200, map[string]any{"message": msg})
}

func (p *Plugin) deleteDomain(w http.ResponseWriter, r *http.Request) {
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	d := normHost(r.PathValue("domain"))
	mc := p.mailcow(c)
	mb, err := mc.Mailboxes(ctx, d)
	if err != nil {
		fail(w, err)
		return
	}
	if len(mb) > 0 {
		plugin.WriteErr(w, 409, fmt.Sprintf("%s still has %d mailbox(es); delete them first", d, len(mb)))
		return
	}
	if err := mc.DeleteDomain(ctx, d); err != nil {
		fail(w, err)
		return
	}
	p.setPending(ctx, d, false)
	msg := "Deleted mail domain " + d
	if r.URL.Query().Get("ses") == "1" {
		if s := p.ses(c); s != nil {
			if err := s.DeleteIdentity(ctx, d); err != nil {
				msg += " (removing it from SES failed: " + err.Error() + ")"
			} else {
				msg += " and its SES identity"
			}
		}
	}
	p.host.Event(ctx, "info", msg)
	plugin.WriteJSON(w, 200, map[string]any{"message": msg + ". DNS records were left as they are."})
}

var localRe = regexp.MustCompile(`^[a-z0-9!#$%&'*+/=?^_{|}~-]+(\.[a-z0-9!#$%&'*+/=?^_{|}~-]+)*$`)

// Password returns a random password for a new mailbox.
func Password() string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

func (p *Plugin) addMailbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LocalPart string `json:"local_part"`
		Name      string `json:"name"`
		Password  string `json:"password"`
		QuotaMB   int    `json:"quota_mb"`
	}
	if err := plugin.ReadJSON(r, &req); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	d := normHost(r.PathValue("domain"))
	local := strings.ToLower(strings.TrimSpace(req.LocalPart))
	if !localRe.MatchString(local) {
		plugin.WriteErr(w, 400, "the part before @ is not valid")
		return
	}
	pw, generated := req.Password, false
	if pw == "" {
		pw, generated = Password(), true
	}
	q := req.QuotaMB
	if q <= 0 {
		q = c.Int("mailbox_quota_mb")
	}
	if err := p.mailcow(c).AddMailbox(r.Context(), local, d, strings.TrimSpace(req.Name), pw, q); err != nil {
		fail(w, err)
		return
	}
	p.host.Event(r.Context(), "info", "Added mailbox "+local+"@"+d)
	out := map[string]any{"address": local + "@" + d}
	if generated {
		out["password"] = pw
	}
	plugin.WriteJSON(w, 200, out)
}

func (p *Plugin) editMailbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string `json:"name"`
		Password *string `json:"password"`
		QuotaMB  *int    `json:"quota_mb"`
		Active   *bool   `json:"active"`
	}
	if err := plugin.ReadJSON(r, &req); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	addr := strings.ToLower(r.PathValue("address"))
	attr := map[string]any{}
	out := map[string]any{"address": addr}
	if req.Name != nil {
		attr["name"] = strings.TrimSpace(*req.Name)
	}
	if req.QuotaMB != nil {
		attr["quota"] = fmt.Sprint(*req.QuotaMB)
	}
	if req.Active != nil {
		attr["active"] = map[bool]string{true: "1", false: "0"}[*req.Active]
	}
	if req.Password != nil {
		pw := *req.Password
		if pw == "" {
			pw = Password()
			out["password"] = pw
		}
		attr["password"], attr["password2"] = pw, pw
	}
	if len(attr) == 0 {
		plugin.WriteErr(w, 400, "nothing to change")
		return
	}
	if err := p.mailcow(c).EditMailbox(r.Context(), addr, attr); err != nil {
		fail(w, err)
		return
	}
	if req.Password != nil {
		p.host.Event(r.Context(), "info", "Changed the password of "+addr)
	}
	plugin.WriteJSON(w, 200, out)
}

func (p *Plugin) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	addr := strings.ToLower(r.PathValue("address"))
	if err := p.mailcow(c).DeleteMailbox(r.Context(), addr); err != nil {
		fail(w, err)
		return
	}
	p.host.Event(r.Context(), "warn", "Deleted mailbox "+addr+" and its mail")
	plugin.WriteJSON(w, 200, map[string]any{"ok": true})
}

func (p *Plugin) addAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LocalPart string `json:"local_part"` // "" or "*" = catch-all
		Goto      string `json:"goto"`
	}
	if err := plugin.ReadJSON(r, &req); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	d := normHost(r.PathValue("domain"))
	local := strings.ToLower(strings.TrimSpace(req.LocalPart))
	addr := "@" + d // catch-all
	if local != "" && local != "*" {
		if !localRe.MatchString(local) {
			plugin.WriteErr(w, 400, "the part before @ is not valid")
			return
		}
		addr = local + "@" + d
	}
	var dest []string
	for _, g := range strings.FieldsFunc(req.Goto, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if !strings.Contains(g, "@") {
			plugin.WriteErr(w, 400, fmt.Sprintf("%q is not an email address", g))
			return
		}
		dest = append(dest, strings.ToLower(g))
	}
	if len(dest) == 0 {
		plugin.WriteErr(w, 400, "say where the mail should go")
		return
	}
	if err := p.mailcow(c).AddAlias(r.Context(), addr, strings.Join(dest, ",")); err != nil {
		fail(w, err)
		return
	}
	p.host.Event(r.Context(), "info", "Added alias "+addr+" → "+strings.Join(dest, ", "))
	plugin.WriteJSON(w, 200, map[string]any{"address": addr})
}

func (p *Plugin) deleteAlias(w http.ResponseWriter, r *http.Request) {
	c, ok := p.config(w, r)
	if !ok {
		return
	}
	mc := p.mailcow(c)
	d := normHost(r.PathValue("domain"))
	id := r.PathValue("id")
	// Only delete aliases that belong to this domain.
	al, err := mc.Aliases(r.Context(), d)
	if err != nil {
		fail(w, err)
		return
	}
	var addr string
	for _, a := range al {
		if a.ID == id {
			addr = a.Address
		}
	}
	if addr == "" {
		plugin.WriteErr(w, 404, "no such alias")
		return
	}
	if err := mc.DeleteAlias(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	p.host.Event(r.Context(), "info", "Deleted alias "+addr)
	plugin.WriteJSON(w, 200, map[string]any{"ok": true})
}
