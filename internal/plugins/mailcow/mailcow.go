package mailcow

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to the mailcow API (/api/v1, X-API-Key header).
type Client struct {
	URL      string
	Key      string
	Insecure bool
	HTTP     *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user's choice for a self-signed mailcow
	}
	c.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: tr}
	return c.HTTP
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if c.URL == "" || c.Key == "" {
		return errors.New("set the mailcow address and API key on the Plugins page")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+"/api/v1/"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("mailcow: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return errors.New("mailcow refused the API key (check it is a read-write key and this server's IP is allowed)")
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("mailcow: HTTP %d: %s", res.StatusCode, short(b))
	}
	// Errors come back with HTTP 200 as {"type":"error","msg":...}.
	var e struct {
		Type string `json:"type"`
		Msg  any    `json:"msg"`
	}
	if json.Unmarshal(b, &e) == nil && (e.Type == "error" || e.Type == "danger") {
		return fmt.Errorf("mailcow: %s", msgText(e.Msg))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("mailcow: unexpected reply: %s", short(b))
	}
	return nil
}

// change runs an add/edit/delete call. These answer with a list of
// {"type":"success"|"danger"|"error","msg":...}.
func (c *Client) change(ctx context.Context, path string, body any) error {
	var res json.RawMessage
	if err := c.do(ctx, "POST", path, body, &res); err != nil {
		return err
	}
	var list []struct {
		Type string `json:"type"`
		Msg  any    `json:"msg"`
	}
	if json.Unmarshal(res, &list) != nil {
		var one struct {
			Type string `json:"type"`
			Msg  any    `json:"msg"`
		}
		if json.Unmarshal(res, &one) == nil && one.Type != "" {
			list = append(list, one)
		}
	}
	var errs []string
	for _, r := range list {
		if r.Type == "danger" || r.Type == "error" {
			errs = append(errs, msgText(r.Msg))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mailcow: %s", strings.Join(errs, "; "))
	}
	return nil
}

func msgText(m any) string {
	switch v := m.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, p := range v {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(m)
}

func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// num reads mailcow's numbers, which arrive as numbers or strings.
func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	case bool:
		if n {
			return 1
		}
	}
	return 0
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	case float64:
		return strconv.FormatInt(int64(s), 10)
	}
	return fmt.Sprint(v)
}

// Version returns mailcow's version (also proves the key works).
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	err := c.do(ctx, "GET", "get/status/version", nil, &v)
	return v.Version, err
}

// Domain is a mailcow mail domain.
type Domain struct {
	Name          string `json:"domain"`
	Description   string `json:"description"`
	Active        bool   `json:"active"`
	Mailboxes     int64  `json:"mailboxes"`
	MaxMailboxes  int64  `json:"max_mailboxes"`
	Aliases       int64  `json:"aliases"`
	MaxAliases    int64  `json:"max_aliases"`
	QuotaMB       int64  `json:"quota_mb"`     // whole domain
	DefQuotaMB    int64  `json:"def_quota_mb"` // new mailboxes
	MaxQuotaMB    int64  `json:"max_quota_mb"` // per mailbox
	BytesUsed     int64  `json:"bytes_used"`   // all mailboxes
	RelayhostID   string `json:"relayhost_id"` // "0" = none
	Messages      int64  `json:"messages"`
	BackupMX      bool   `json:"backup_mx"`
	SESTransport  bool   `json:"ses_transport"`  // filled by the plugin
	TransportHost string `json:"transport_host"` // filled by the plugin
	Site          string `json:"site,omitempty"` // Berth site with this domain
	Pending       bool   `json:"pending,omitempty"`
}

func domainFrom(m map[string]any) Domain {
	const mb = 1 << 20
	return Domain{
		Name:         str(m["domain_name"]),
		Description:  str(m["description"]),
		Active:       num(m["active"]) == 1,
		Mailboxes:    num(m["mboxes_in_domain"]),
		MaxMailboxes: num(m["max_num_mboxes_for_domain"]),
		Aliases:      num(m["aliases_in_domain"]),
		MaxAliases:   num(m["max_num_aliases_for_domain"]),
		QuotaMB:      num(m["max_quota_for_domain"]) / mb,
		DefQuotaMB:   num(m["def_quota_for_mbox"]) / mb,
		MaxQuotaMB:   num(m["max_quota_for_mbox"]) / mb,
		BytesUsed:    num(m["bytes_total"]),
		Messages:     num(m["msgs_total"]),
		RelayhostID:  str(m["relayhost"]),
		BackupMX:     num(m["backupmx"]) == 1,
	}
}

// Domains lists every mail domain.
func (c *Client) Domains(ctx context.Context) ([]Domain, error) {
	var raw []map[string]any
	if err := c.do(ctx, "GET", "get/domain/all", nil, &raw); err != nil {
		return nil, err
	}
	out := []Domain{}
	for _, m := range raw {
		out = append(out, domainFrom(m))
	}
	return out, nil
}

// ErrNoDomain is returned when mailcow does not have the domain.
var ErrNoDomain = errors.New("mailcow has no such domain")

// Domain loads one mail domain.
func (c *Client) Domain(ctx context.Context, name string) (Domain, error) {
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "get/domain/"+url.PathEscape(name), nil, &raw); err != nil {
		return Domain{}, err
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || str(m["domain_name"]) == "" {
		return Domain{}, ErrNoDomain
	}
	return domainFrom(m), nil
}

// NewDomain is what AddDomain needs.
type NewDomain struct {
	Name         string
	Description  string
	MaxMailboxes int
	MaxAliases   int
	QuotaMB      int
	DefQuotaMB   int
	MaxQuotaMB   int
}

// AddDomain creates a mail domain.
func (c *Client) AddDomain(ctx context.Context, d NewDomain) error {
	return c.change(ctx, "add/domain", map[string]any{
		"domain":               d.Name,
		"description":          d.Description,
		"active":               "1",
		"aliases":              strconv.Itoa(d.MaxAliases),
		"mailboxes":            strconv.Itoa(d.MaxMailboxes),
		"defquota":             strconv.Itoa(d.DefQuotaMB),
		"maxquota":             strconv.Itoa(d.MaxQuotaMB),
		"quota":                strconv.Itoa(d.QuotaMB),
		"backupmx":             "0",
		"relay_all_recipients": "0",
		"restart_sogo":         "1",
	})
}

// EditDomain changes domain attributes (e.g. {"relayhost":"3"}).
func (c *Client) EditDomain(ctx context.Context, name string, attr map[string]any) error {
	return c.change(ctx, "edit/domain", map[string]any{"items": []string{name}, "attr": attr})
}

// DeleteDomain removes an empty mail domain.
func (c *Client) DeleteDomain(ctx context.Context, name string) error {
	return c.change(ctx, "delete/domain", []string{name})
}

// Mailbox is a mailcow mailbox.
type Mailbox struct {
	Address   string `json:"address"`
	LocalPart string `json:"local_part"`
	Domain    string `json:"domain"`
	Name      string `json:"name"`
	QuotaMB   int64  `json:"quota_mb"` // 0 = unlimited
	BytesUsed int64  `json:"bytes_used"`
	Messages  int64  `json:"messages"`
	Active    bool   `json:"active"`
	LastLogin int64  `json:"last_login,omitempty"` // unix time of the last IMAP login
}

// Mailboxes lists the mailboxes of one domain.
func (c *Client) Mailboxes(ctx context.Context, domain string) ([]Mailbox, error) {
	var raw []map[string]any
	if err := c.do(ctx, "GET", "get/mailbox/all", nil, &raw); err != nil {
		return nil, err
	}
	out := []Mailbox{}
	for _, m := range raw {
		if !strings.EqualFold(str(m["domain"]), domain) {
			continue
		}
		out = append(out, Mailbox{
			Address:   str(m["username"]),
			LocalPart: str(m["local_part"]),
			Domain:    str(m["domain"]),
			Name:      str(m["name"]),
			QuotaMB:   num(m["quota"]) / (1 << 20),
			BytesUsed: num(m["quota_used"]),
			Messages:  num(m["messages"]),
			Active:    num(m["active"]) == 1,
			LastLogin: num(m["last_imap_login"]),
		})
	}
	return out, nil
}

// AddMailbox creates a mailbox.
func (c *Client) AddMailbox(ctx context.Context, local, domain, name, password string, quotaMB int) error {
	return c.change(ctx, "add/mailbox", map[string]any{
		"local_part": local,
		"domain":     domain,
		"name":       name,
		"quota":      strconv.Itoa(quotaMB),
		"password":   password,
		"password2":  password,
		"active":     "1",
	})
}

// EditMailbox changes mailbox attributes.
func (c *Client) EditMailbox(ctx context.Context, address string, attr map[string]any) error {
	return c.change(ctx, "edit/mailbox", map[string]any{"items": []string{address}, "attr": attr})
}

// DeleteMailbox removes a mailbox and its mail.
func (c *Client) DeleteMailbox(ctx context.Context, address string) error {
	return c.change(ctx, "delete/mailbox", []string{address})
}

// Alias is a mailcow alias.
type Alias struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Goto    string `json:"goto"`
	Active  bool   `json:"active"`
}

// Aliases lists the aliases of one domain.
func (c *Client) Aliases(ctx context.Context, domain string) ([]Alias, error) {
	var raw []map[string]any
	if err := c.do(ctx, "GET", "get/alias/all", nil, &raw); err != nil {
		return nil, err
	}
	out := []Alias{}
	for _, m := range raw {
		if !strings.EqualFold(str(m["domain"]), domain) {
			continue
		}
		out = append(out, Alias{ID: str(m["id"]), Address: str(m["address"]), Goto: str(m["goto"]), Active: num(m["active"]) == 1})
	}
	return out, nil
}

// AddAlias creates an alias (goto may hold several addresses, comma separated).
func (c *Client) AddAlias(ctx context.Context, address, gotoAddr string) error {
	return c.change(ctx, "add/alias", map[string]any{"address": address, "goto": gotoAddr, "active": "1"})
}

// DeleteAlias removes an alias by ID.
func (c *Client) DeleteAlias(ctx context.Context, id string) error {
	return c.change(ctx, "delete/alias", []string{id})
}

// DKIM is a domain's mailcow DKIM key.
type DKIM struct {
	Selector string `json:"selector"`
	TXT      string `json:"txt"` // v=DKIM1;k=rsa;...
}

// DKIM returns the domain's DKIM key ("" TXT when there is none).
func (c *Client) DKIM(ctx context.Context, domain string) (DKIM, error) {
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "get/dkim/"+url.PathEscape(domain), nil, &raw); err != nil {
		return DKIM{}, err
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return DKIM{}, nil
	}
	return DKIM{Selector: str(m["dkim_selector"]), TXT: str(m["dkim_txt"])}, nil
}

// AddDKIM generates a 2048-bit DKIM key for the domain.
func (c *Client) AddDKIM(ctx context.Context, domain, selector string) error {
	return c.change(ctx, "add/dkim", map[string]any{"domains": domain, "dkim_selector": selector, "key_size": "2048"})
}

// Relayhost is a mailcow sender-dependent transport.
type Relayhost struct {
	ID       string
	Hostname string // host:port
	Username string
	Password string
	Active   bool
}

// Relayhosts lists sender-dependent transports.
func (c *Client) Relayhosts(ctx context.Context) ([]Relayhost, error) {
	var raw []map[string]any
	if err := c.do(ctx, "GET", "get/relayhost/all", nil, &raw); err != nil {
		return nil, err
	}
	var out []Relayhost
	for _, m := range raw {
		out = append(out, Relayhost{ID: str(m["id"]), Hostname: str(m["hostname"]), Username: str(m["username"]), Password: str(m["password"]), Active: num(m["active"]) == 1})
	}
	return out, nil
}

// AddRelayhost creates a sender-dependent transport.
func (c *Client) AddRelayhost(ctx context.Context, hostname, user, pass string) error {
	return c.change(ctx, "add/relayhost", map[string]any{"hostname": hostname, "username": user, "password": pass})
}

// EditRelayhost changes a sender-dependent transport.
func (c *Client) EditRelayhost(ctx context.Context, id string, attr map[string]any) error {
	return c.change(ctx, "edit/relayhost", map[string]any{"items": []string{id}, "attr": attr})
}
