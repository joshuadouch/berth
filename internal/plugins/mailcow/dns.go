package mailcow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Record is a DNS record a mail domain needs.
type Record struct {
	Type     string `json:"type"`
	Name     string `json:"name"` // fully qualified, no trailing dot
	Content  string `json:"content"`
	Priority int    `json:"priority,omitempty"`
	Purpose  string `json:"purpose"`
	// Prefix marks TXT records that share a name with others (SPF, DMARC):
	// only TXT records starting with it compete with this one.
	Prefix string `json:"-"`
	// KeepAny accepts whatever record already fills the slot (DMARC policy is
	// the owner's choice).
	KeepAny bool `json:"-"`

	Status   string   `json:"status"`             // ok, created, missing, conflict, error
	Existing []string `json:"existing,omitempty"` // what is there instead
	Error    string   `json:"error,omitempty"`
}

// Plan lists the records for a mail domain.
type Plan struct {
	Domain   string
	MailHost string   // mailcow's hostname (MX, autodiscover)
	DKIM     DKIM     // mailcow's DKIM key
	SES      bool     // SES identity in use
	Tokens   []string // SES Easy DKIM tokens
	Region   string
	MailFrom string // custom MAIL FROM label, e.g. "bounce"
	DMARC    string // TXT for _dmarc when there is none
}

// Records returns the records the domain needs.
func (p Plan) Records() []Record {
	d := strings.ToLower(p.Domain)
	var out []Record
	if p.MailHost != "" {
		out = append(out, Record{Type: "MX", Name: d, Content: p.MailHost, Priority: 10, Purpose: "Delivers mail for the domain to mailcow"})
	}
	spf := "v=spf1 mx"
	if p.SES {
		spf += " include:amazonses.com"
	}
	out = append(out, Record{Type: "TXT", Name: d, Content: spf + " ~all", Prefix: "v=spf1", Purpose: "SPF: who may send as the domain"})
	if p.DKIM.TXT != "" {
		sel := p.DKIM.Selector
		if sel == "" {
			sel = "dkim"
		}
		out = append(out, Record{Type: "TXT", Name: sel + "._domainkey." + d, Content: p.DKIM.TXT, Purpose: "mailcow DKIM key"})
	}
	if p.SES {
		for _, t := range p.Tokens {
			out = append(out, Record{Type: "CNAME", Name: t + "._domainkey." + d, Content: t + ".dkim.amazonses.com", Purpose: "Amazon SES DKIM (verifies the domain in SES)"})
		}
		if p.MailFrom != "" && p.Region != "" {
			mf := p.MailFrom + "." + d
			out = append(out,
				Record{Type: "MX", Name: mf, Content: "feedback-smtp." + p.Region + ".amazonses.com", Priority: 10, Purpose: "SES custom MAIL FROM (bounces)"},
				Record{Type: "TXT", Name: mf, Content: "v=spf1 include:amazonses.com ~all", Prefix: "v=spf1", Purpose: "SPF for the SES MAIL FROM domain"},
			)
		}
	}
	if p.DMARC != "" {
		out = append(out, Record{Type: "TXT", Name: "_dmarc." + d, Content: p.DMARC, Prefix: "v=DMARC1", KeepAny: true, Purpose: "DMARC policy (an existing one is kept)"})
	}
	if p.MailHost != "" && !strings.EqualFold(p.MailHost, "autodiscover."+d) {
		out = append(out,
			Record{Type: "CNAME", Name: "autodiscover." + d, Content: p.MailHost, Purpose: "Outlook and phone mail set-up"},
			Record{Type: "CNAME", Name: "autoconfig." + d, Content: p.MailHost, Purpose: "Thunderbird mail set-up"},
		)
	}
	return out
}

// DNS reads (and maybe writes) DNS records.
type DNS interface {
	// Lookup returns the contents of records of a type at a name. MX
	// contents are "priority host".
	Lookup(ctx context.Context, typ, name string) ([]string, error)
}

// Writer can also create records.
type Writer interface {
	DNS
	Create(ctx context.Context, r Record) error
}

func normHost(s string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".") }

// normTXT joins quoted chunks and drops spaces so long TXT records compare equal
// however they were split.
func normTXT(s string) string {
	s = strings.ReplaceAll(s, `" "`, "")
	s = strings.ReplaceAll(s, `"`, "")
	return strings.Join(strings.Fields(s), "")
}

// same reports whether existing record content equals the wanted record.
func same(r Record, got string) bool {
	switch r.Type {
	case "MX":
		f := strings.Fields(got)
		if len(f) != 2 {
			return false
		}
		return normHost(f[1]) == normHost(r.Content)
	case "CNAME":
		return normHost(got) == normHost(r.Content)
	default:
		return strings.EqualFold(normTXT(got), normTXT(r.Content))
	}
}

// slot keeps the existing records that compete with r.
func slot(r Record, got []string) []string {
	if r.Type != "TXT" || r.Prefix == "" {
		return got
	}
	var out []string
	for _, g := range got {
		if strings.HasPrefix(strings.ToLower(normTXT(g)), strings.ToLower(r.Prefix)) {
			out = append(out, g)
		}
	}
	return out
}

// Check fills each record's status. With a Writer and create set, missing
// records are added; records that clash with existing ones are never changed.
func Check(ctx context.Context, d DNS, recs []Record, create bool) []Record {
	w, canWrite := d.(Writer)
	for i := range recs {
		r := &recs[i]
		got, err := d.Lookup(ctx, r.Type, r.Name)
		if err != nil {
			r.Status, r.Error = "error", err.Error()
			continue
		}
		got = slot(*r, got)
		switch {
		case len(got) == 0:
			r.Status = "missing"
		case r.KeepAny:
			r.Status = "ok"
		default:
			r.Status = "conflict"
			for _, g := range got {
				if same(*r, g) {
					r.Status = "ok"
				}
			}
			if r.Status == "conflict" {
				r.Existing = got
			}
		}
		if r.Status == "missing" && create && canWrite {
			if err := w.Create(ctx, *r); err != nil {
				r.Status, r.Error = "error", err.Error()
			} else {
				r.Status = "created"
			}
		}
	}
	return recs
}

// Resolver checks records with ordinary DNS lookups.
type Resolver struct{ R *net.Resolver }

// Lookup implements DNS.
func (r Resolver) Lookup(ctx context.Context, typ, name string) ([]string, error) {
	res := r.R
	if res == nil {
		res = net.DefaultResolver
	}
	var out []string
	var err error
	switch typ {
	case "MX":
		var mx []*net.MX
		mx, err = res.LookupMX(ctx, name)
		for _, m := range mx {
			out = append(out, fmt.Sprintf("%d %s", m.Pref, normHost(m.Host)))
		}
	case "TXT":
		out, err = res.LookupTXT(ctx, name)
	case "CNAME":
		var c string
		c, err = res.LookupCNAME(ctx, name)
		if err == nil && normHost(c) != normHost(name) {
			out = []string{normHost(c)}
		}
	default:
		return nil, fmt.Errorf("cannot look up %s records", typ)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	return out, err
}

// Cloudflare reads and creates records through the Cloudflare API.
type Cloudflare struct {
	Token string
	Base  string // tests; default https://api.cloudflare.com/client/v4
	HTTP  *http.Client
	zones map[string]string
}

func (c *Cloudflare) call(ctx context.Context, method, path string, body, out any) error {
	base := c.Base
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare: %w", err)
	}
	defer res.Body.Close()
	var env struct {
		Success bool                       `json:"success"`
		Errors  []struct{ Message string } `json:"errors"`
		Result  json.RawMessage            `json:"result"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("Cloudflare: HTTP %d: %s", res.StatusCode, short(b))
	}
	if !env.Success {
		var msgs []string
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("Cloudflare: %s", strings.Join(msgs, "; "))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Verify checks the token.
func (c *Cloudflare) Verify(ctx context.Context) error {
	return c.call(ctx, "GET", "/user/tokens/verify", nil, nil)
}

// zone finds the Cloudflare zone holding name.
func (c *Cloudflare) zone(ctx context.Context, name string) (string, error) {
	if c.zones == nil {
		c.zones = map[string]string{}
	}
	labels := strings.Split(normHost(name), ".")
	for i := 0; i < len(labels)-1; i++ {
		z := strings.Join(labels[i:], ".")
		if id, ok := c.zones[z]; ok {
			return id, nil
		}
		var res []struct {
			ID string `json:"id"`
		}
		if err := c.call(ctx, "GET", "/zones?name="+url.QueryEscape(z), nil, &res); err != nil {
			return "", err
		}
		if len(res) > 0 {
			c.zones[z] = res[0].ID
			return res[0].ID, nil
		}
	}
	return "", fmt.Errorf("the Cloudflare token cannot see a zone for %s", name)
}

// Lookup implements DNS.
func (c *Cloudflare) Lookup(ctx context.Context, typ, name string) ([]string, error) {
	zid, err := c.zone(ctx, name)
	if err != nil {
		return nil, err
	}
	var res []struct {
		Content  string `json:"content"`
		Priority *int   `json:"priority"`
	}
	q := url.Values{"type": {typ}, "name": {normHost(name)}, "per_page": {"100"}}
	if err := c.call(ctx, "GET", "/zones/"+zid+"/dns_records?"+q.Encode(), nil, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res {
		if typ == "MX" {
			p := 0
			if r.Priority != nil {
				p = *r.Priority
			}
			out = append(out, fmt.Sprintf("%d %s", p, normHost(r.Content)))
			continue
		}
		out = append(out, r.Content)
	}
	return out, nil
}

// Create implements Writer. Mail records are never proxied.
func (c *Cloudflare) Create(ctx context.Context, r Record) error {
	zid, err := c.zone(ctx, r.Name)
	if err != nil {
		return err
	}
	body := map[string]any{"type": r.Type, "name": r.Name, "content": r.Content, "ttl": 1, "comment": "Berth mailcow plugin"}
	if r.Type == "CNAME" {
		body["proxied"] = false
	}
	if r.Type == "TXT" {
		body["content"] = quoteTXT(r.Content)
	}
	if r.Type == "MX" {
		body["priority"] = r.Priority
	}
	return c.call(ctx, "POST", "/zones/"+zid+"/dns_records", body, nil)
}

// quoteTXT splits a TXT value into quoted 255-byte strings, which Cloudflare
// accepts for long values like DKIM keys.
func quoteTXT(s string) string {
	if len(s) <= 255 {
		return `"` + s + `"`
	}
	var parts []string
	for len(s) > 255 {
		parts = append(parts, `"`+s[:255]+`"`)
		s = s[255:]
	}
	parts = append(parts, `"`+s+`"`)
	return strings.Join(parts, " ")
}
