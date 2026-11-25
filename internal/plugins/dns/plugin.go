// Package dns is the DNS plugin: zones on Berth's own PowerDNS nameservers,
// edited by admins and hosting customers, with records added for new sites.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
	"golang.org/x/net/publicsuffix"
)

// ID is the plugin's ID.
const ID = "dns"

// Owner records which account a zone belongs to.
type Owner struct {
	Account string    `json:"account"`
	Created time.Time `json:"created"`
}

// Plugin is the DNS plugin.
type Plugin struct {
	host *plugin.Host
	mu   sync.Mutex // guards the zone list
	// HTTP, LookupNS and LookupIP are replaced in tests.
	HTTP     *http.Client
	LookupNS func(ctx context.Context, name string) ([]string, error)
	LookupIP func(ctx context.Context, name string) ([]string, error)
}

// New returns the plugin.
func New() *Plugin {
	return &Plugin{
		LookupNS: func(ctx context.Context, name string) ([]string, error) {
			ns, err := net.DefaultResolver.LookupNS(ctx, name)
			var out []string
			for _, n := range ns {
				out = append(out, strings.ToLower(strings.TrimSuffix(n.Host, ".")))
			}
			return out, err
		},
		LookupIP: func(ctx context.Context, name string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, name)
		},
	}
}

// Info implements plugin.Plugin.
func (p *Plugin) Info() plugin.Info {
	return plugin.Info{
		ID:          ID,
		Name:        "DNS hosting",
		Description: "Host customers' domains on your own PowerDNS nameservers. Accounts whose plan includes DNS domains (or that are picked here) get a DNS page to add zones and edit records, and new sites get their zone and A and AAAA records automatically.",
		Version:     "1.0",
		Icon:        "globe",
		NavLabel:    "DNS",
		PerAccount:  true,
		Fields: []plugin.Field{
			{Key: "api_url", Label: "API address", Type: plugin.Text, Section: "Primary nameserver", Required: true, Placeholder: "http://10.0.30.53:8081", Help: "PowerDNS's API on the primary. The installer prints it."},
			{Key: "api_key", Label: "API key", Type: plugin.Secret, Section: "Primary nameserver", Required: true},
			{Key: "secondary_url", Label: "API address", Type: plugin.Text, Section: "Secondary nameserver", Placeholder: "http://203.0.113.53:8081", Help: "Berth adds and removes each zone there too. Leave blank if your secondary picks up zones by itself."},
			{Key: "secondary_key", Label: "API key", Type: plugin.Secret, Section: "Secondary nameserver"},
			{Key: "primary_ip", Label: "Primary's address for zone transfers", Type: plugin.Text, Section: "Secondary nameserver", Placeholder: "198.51.100.53", Help: "Where the secondary fetches zones from."},
			{Key: "nameservers", Label: "Nameservers", Type: plugin.Text, Section: "Zones", Required: true, Placeholder: "ns1.example.net, ns2.example.net", Help: "The names customers set at their registrar, primary first."},
			{Key: "hostmaster", Label: "Hostmaster email", Type: plugin.Text, Section: "Zones", Placeholder: "hostmaster@example.net", Help: "Goes in every zone's SOA record."},
			{Key: "ttl", Label: "Default TTL (seconds)", Type: plugin.Number, Section: "Zones", Default: 3600},
			{Key: "ipv4", Label: "This server's public IPv4", Type: plugin.Text, Section: "Site records", Placeholder: "203.0.113.10", Help: "Blank uses the address under Settings → Customers."},
			{Key: "ipv6", Label: "This server's public IPv6", Type: plugin.Text, Section: "Site records", Placeholder: "2001:db8::10"},
			{Key: "auto_records", Label: "Add records for new sites", Type: plugin.Bool, Section: "Site records", Default: true, Help: "When a site's domain is in one of its account's zones, point it at this server (unless the name already has a record)."},
			{Key: "auto_zones", Label: "Add a zone for new sites", Type: plugin.Bool, Section: "Site records", Default: true, Help: "When a customer makes a site on a domain that isn't hosted here yet, add the domain as a zone (within their plan) so they only have to set the nameservers."},
		},
	}
}

// Validate implements plugin.Validator.
func (p *Plugin) Validate(c plugin.Config) error {
	if _, err := nameservers(c); err != nil {
		return err
	}
	if v := c.Str("primary_ip"); v != "" {
		if _, err := netip.ParseAddr(v); err != nil {
			if _, err := netip.ParseAddrPort(v); err != nil {
				return fmt.Errorf("%q is not an IP address", v)
			}
		}
	}
	for _, k := range []string{"ipv4", "ipv6"} {
		if v := c.Str(k); v != "" {
			if _, err := netip.ParseAddr(v); err != nil {
				return fmt.Errorf("%q is not an IP address", v)
			}
		}
	}
	if v := c.Str("ipv4"); v != "" && !netip.MustParseAddr(v).Is4() {
		return errors.New("the IPv4 address is IPv6")
	}
	if v := c.Str("ipv6"); v != "" && !netip.MustParseAddr(v).Is6() {
		return errors.New("the IPv6 address is IPv4")
	}
	if c.Str("secondary_url") != "" && c.Str("primary_ip") == "" {
		return errors.New("the secondary needs the primary's address for zone transfers")
	}
	return nil
}

func nameservers(c plugin.Config) ([]string, error) {
	var out []string
	for _, n := range strings.FieldsFunc(c.Str("nameservers"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		n = strings.TrimSuffix(strings.ToLower(n), ".")
		if !validName(n, false) || !strings.Contains(n, ".") {
			return nil, fmt.Errorf("%q is not a nameserver name", n)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("add your nameservers, like ns1.example.net, ns2.example.net")
	}
	return out, nil
}

func (p *Plugin) primary(c plugin.Config) *PDNS {
	return &PDNS{URL: c.Str("api_url"), Key: c.Str("api_key"), HTTP: p.HTTP}
}

func (p *Plugin) secondary(c plugin.Config) *PDNS {
	if c.Str("secondary_url") == "" {
		return nil
	}
	return &PDNS{URL: c.Str("secondary_url"), Key: c.Str("secondary_key"), HTTP: p.HTTP}
}

// Test implements plugin.Tester.
func (p *Plugin) Test(ctx context.Context, c plugin.Config) (string, error) {
	v, err := p.primary(c).Version(ctx)
	if err != nil {
		return "", fmt.Errorf("primary: %w", err)
	}
	msg := "Primary runs PowerDNS " + v
	if s := p.secondary(c); s != nil {
		v2, err := s.Version(ctx)
		if err != nil {
			return "", fmt.Errorf("secondary: %w", err)
		}
		msg += ", secondary runs PowerDNS " + v2
	}
	return msg + ".", nil
}

// Init implements plugin.Plugin.
func (p *Plugin) Init(h *plugin.Host) http.Handler {
	p.host = h
	m := http.NewServeMux()
	m.HandleFunc("GET /info", p.info)
	m.HandleFunc("GET /zones", p.listZones)
	m.HandleFunc("POST /zones", p.createZone)
	m.HandleFunc("GET /zones/{zone}", p.getZone)
	m.HandleFunc("DELETE /zones/{zone}", p.deleteZone)
	m.HandleFunc("PUT /zones/{zone}/records", p.putRecords)
	m.HandleFunc("POST /zones/{zone}/delete-records", p.deleteRecords)
	m.HandleFunc("PUT /zones/{zone}/account", p.moveZone)
	m.HandleFunc("POST /sync", p.sync)
	m.HandleFunc("GET /sites/{name}", p.siteStatus)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { plugin.WriteErr(w, 404, "no such endpoint") })
	return m
}

// config loads the settings, with the server's public addresses from
// Settings → Customers when the plugin has none of its own.
func (p *Plugin) config(ctx context.Context) (plugin.Config, error) {
	c, err := p.host.Config(ctx)
	if err != nil {
		return c, err
	}
	if st, err := p.host.Store.Settings(ctx); err == nil {
		if c.Str("ipv4") == "" && st.Brand.IPv4 != "" {
			c["ipv4"] = st.Brand.IPv4
		}
		if c.Str("ipv6") == "" && st.Brand.IPv6 != "" {
			c["ipv6"] = st.Brand.IPv6
		}
	}
	return c, nil
}

// owners loads which account owns each zone.
func (p *Plugin) owners(ctx context.Context) map[string]Owner {
	o := map[string]Owner{}
	_ = p.host.GetData(ctx, "zones", &o)
	if o == nil {
		o = map[string]Owner{}
	}
	return o
}

// zoneFor finds the account's zone a hostname belongs to (the longest match).
func zoneFor(owners map[string]Owner, account, host string) string {
	best := ""
	for z, o := range owners {
		if o.Account == account && (host == z || strings.HasSuffix(host, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best
}

// mayUse loads the zone named in the path when the user may edit it.
func (p *Plugin) mayUse(w http.ResponseWriter, r *http.Request) (string, Owner, plugin.Config, bool) {
	zone := strings.TrimSuffix(strings.ToLower(r.PathValue("zone")), ".")
	o, ok := p.owners(r.Context())[zone]
	u := plugin.User(r)
	if !ok && u.IsAdmin() {
		// Admins may look at zones Berth did not create.
		o, ok = Owner{}, true
	}
	if !ok || !u.IsAdmin() && o.Account != u.AccountID {
		plugin.WriteErr(w, 404, "no such zone")
		return "", o, nil, false
	}
	c, err := p.config(r.Context())
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return "", o, nil, false
	}
	return zone, o, c, true
}

type infoView struct {
	Nameservers []string `json:"nameservers"`
	IPv4        string   `json:"ipv4"`
	IPv6        string   `json:"ipv6"`
	Secondary   bool     `json:"secondary"`
}

func (p *Plugin) info(w http.ResponseWriter, r *http.Request) {
	c, err := p.config(r.Context())
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	ns, _ := nameservers(c)
	if ns == nil {
		ns = []string{}
	}
	plugin.WriteJSON(w, 200, infoView{Nameservers: ns, IPv4: c.Str("ipv4"), IPv6: c.Str("ipv6"), Secondary: c.Str("secondary_url") != ""})
}

type zoneRow struct {
	Name    string    `json:"name"`
	Account string    `json:"account_id"`
	Created time.Time `json:"created,omitempty"`
	Serial  int64     `json:"serial"`
	// Unmanaged zones exist in PowerDNS but no account owns them (admins only).
	Unmanaged bool `json:"unmanaged,omitempty"`
}

func (p *Plugin) listZones(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := plugin.User(r)
	c, err := p.config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	owners := p.owners(ctx)
	rows := []zoneRow{}
	seen := map[string]bool{}
	zones, perr := p.primary(c).Zones(ctx)
	serial := map[string]int64{}
	for _, z := range zones {
		n := strings.TrimSuffix(z.Name, ".")
		serial[n] = z.Serial
		if _, ok := owners[n]; !ok && u.IsAdmin() {
			rows = append(rows, zoneRow{Name: n, Serial: z.Serial, Unmanaged: true})
			seen[n] = true
		}
	}
	for n, o := range owners {
		if u.IsAdmin() || o.Account == u.AccountID {
			rows = append(rows, zoneRow{Name: n, Account: o.Account, Created: o.Created, Serial: serial[n]})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	out := map[string]any{"zones": rows}
	if perr != nil {
		out["error"] = perr.Error()
	}
	plugin.WriteJSON(w, 200, out)
}

func soa(c plugin.Config, zone string, ns []string) string {
	hm := c.Str("hostmaster")
	if hm == "" {
		hm = "hostmaster@" + zone
	}
	if i := strings.Index(hm, "@"); i >= 0 {
		hm = strings.ReplaceAll(hm[:i], ".", `\.`) + "." + hm[i+1:]
	}
	return fmt.Sprintf("%s. %s. 1 10800 3600 1209600 3600", ns[0], strings.TrimSuffix(hm, "."))
}

func ttlOf(c plugin.Config) int {
	if t := c.Int("ttl"); t >= 60 {
		return t
	}
	return 3600
}

// AllowAccount implements plugin.Gate: customers get the DNS page when their
// plan includes DNS domains or they already have a zone here.
func (p *Plugin) AllowAccount(ctx context.Context, account string) bool {
	if pl, ok := p.host.Plan(ctx, account); ok && pl.DNSZones > 0 {
		return true
	}
	for _, o := range p.owners(ctx) {
		if o.Account == account {
			return true
		}
	}
	return false
}

func (p *Plugin) createZone(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := plugin.User(r)
	var in struct {
		Name    string `json:"name"`
		Account string `json:"account_id"`
	}
	if err := plugin.ReadJSON(r, &in); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	zone, err := CheckZoneName(in.Name)
	if err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	account := u.AccountID
	if u.IsAdmin() && in.Account != "" {
		account = in.Account
	}
	c, err := p.config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	warn, err := p.addZone(ctx, c, account, zone, u.IsAdmin())
	if err != nil {
		code := 400
		var pe *plugin.Error
		if errors.As(err, &pe) {
			code = pe.Code
		}
		plugin.WriteErr(w, code, err.Error())
		return
	}
	p.host.Event(ctx, "info", fmt.Sprintf("%s added DNS zone %s", u.Username, zone))
	plugin.WriteJSON(w, 200, map[string]any{"name": zone, "warning": warn})
}

// addZone creates a zone on the nameservers for an account, with records
// for the account's sites already in it. adopt lets an admin take over a
// zone made outside Berth. The warning is for a secondary that failed.
func (p *Plugin) addZone(ctx context.Context, c plugin.Config, account, zone string, adopt bool) (string, error) {
	ns, err := nameservers(c)
	if err != nil {
		return "", &plugin.Error{Code: 400, Msg: "the DNS plugin is not set up yet: " + err.Error()}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	owners := p.owners(ctx)
	if _, ok := owners[zone]; ok {
		return "", &plugin.Error{Code: 409, Msg: zone + " is already hosted here"}
	}
	for z, o := range owners {
		if o.Account != account && (strings.HasSuffix(zone, "."+z) || strings.HasSuffix(z, "."+zone)) {
			return "", &plugin.Error{Code: 409, Msg: zone + " overlaps a zone another account hosts here"}
		}
	}
	if plan, ok := p.host.Plan(ctx, account); ok && plan.DNSZones > 0 {
		n := 0
		for _, o := range owners {
			if o.Account == account {
				n++
			}
		}
		if n >= plan.DNSZones {
			return "", &plugin.Error{Code: 400, Msg: fmt.Sprintf("plan %s allows %d domain%s in DNS", plan.Name, plan.DNSZones, map[bool]string{true: "", false: "s"}[plan.DNSZones == 1])}
		}
	}
	pd := p.primary(c)
	adopted := false
	if _, err := pd.Zone(ctx, zone); err == nil {
		if !adopt {
			return "", &plugin.Error{Code: 409, Msg: zone + " is already hosted here"}
		}
		adopted = true
	} else if !errors.Is(err, ErrNoZone) {
		return "", &plugin.Error{Code: 502, Msg: err.Error()}
	}
	if !adopted {
		var nsRecs []Record
		for _, n := range ns {
			nsRecs = append(nsRecs, Record{Content: n + "."})
		}
		sets := []RRSet{
			{Name: fqdn(zone), Type: "SOA", TTL: ttlOf(c), Records: []Record{{Content: soa(c, zone, ns)}}},
			{Name: fqdn(zone), Type: "NS", TTL: ttlOf(c), Records: nsRecs},
		}
		sets = append(sets, p.siteSets(ctx, c, account, zone, nil)...)
		if err := pd.CreateZone(ctx, Zone{Name: fqdn(zone), Kind: "Master", RRSets: sets, SOAEditAPI: "DEFAULT", APIRectify: true}); err != nil {
			return "", &plugin.Error{Code: 502, Msg: err.Error()}
		}
	}
	owners[zone] = Owner{Account: account, Created: time.Now().UTC()}
	if err := p.host.SetData(ctx, "zones", owners); err != nil {
		return "", &plugin.Error{Code: 500, Msg: err.Error()}
	}
	if err := p.addSecondary(ctx, c, zone); err != nil {
		return "The zone is on the primary, but the secondary could not add it: " + err.Error(), nil
	}
	return "", nil
}

// siteSets returns A/AAAA records for the account's sites inside a new zone,
// skipping names in skip.
func (p *Plugin) siteSets(ctx context.Context, c plugin.Config, account, zone string, skip map[string]bool) []RRSet {
	sites, _ := p.host.Sites(ctx)
	var out []RRSet
	done := map[string]bool{}
	for _, s := range sites {
		if s.AccountID != account || s.Status == model.StatusDeleting {
			continue
		}
		for _, h := range s.Hostnames() {
			if done[h] || skip[h] || h != zone && !strings.HasSuffix(h, "."+zone) {
				continue
			}
			done[h] = true
			out = append(out, addrSets(c, h)...)
		}
	}
	return out
}

func addrSets(c plugin.Config, host string) []RRSet {
	var out []RRSet
	if v := c.Str("ipv4"); v != "" {
		out = append(out, RRSet{Name: fqdn(host), Type: "A", TTL: ttlOf(c), ChangeType: "REPLACE", Records: []Record{{Content: v}}})
	}
	if v := c.Str("ipv6"); v != "" {
		out = append(out, RRSet{Name: fqdn(host), Type: "AAAA", TTL: ttlOf(c), ChangeType: "REPLACE", Records: []Record{{Content: v}}})
	}
	return out
}

func (p *Plugin) addSecondary(ctx context.Context, c plugin.Config, zone string) error {
	s := p.secondary(c)
	if s == nil {
		return nil
	}
	if _, err := s.Zone(ctx, zone); err == nil {
		return s.Retrieve(ctx, zone)
	}
	if err := s.CreateZone(ctx, Zone{Name: fqdn(zone), Kind: "Slave", Masters: []string{c.Str("primary_ip")}}); err != nil {
		return err
	}
	return s.Retrieve(ctx, zone)
}

type zoneView struct {
	Name        string   `json:"name"`
	Account     string   `json:"account_id"`
	Serial      int64    `json:"serial"`
	Sets        []Set    `json:"sets"`
	Nameservers []string `json:"nameservers"`
	TTL         int      `json:"ttl"`
	// Delegation: "ok" (the registrar points at our nameservers), "other"
	// (somewhere else, listed in Found), "none" (not found) or "unknown".
	Delegation string   `json:"delegation"`
	Found      []string `json:"found"`
}

func (p *Plugin) getZone(w http.ResponseWriter, r *http.Request) {
	zone, o, c, ok := p.mayUse(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	z, err := p.primary(c).Zone(ctx, zone)
	if errors.Is(err, ErrNoZone) {
		plugin.WriteErr(w, 404, zone+" is missing on the primary nameserver")
		return
	} else if err != nil {
		plugin.WriteErr(w, 502, err.Error())
		return
	}
	ns, _ := nameservers(c)
	v := zoneView{Name: zone, Account: o.Account, Serial: z.Serial, Sets: toSets(zone, z.RRSets), Nameservers: ns, TTL: ttlOf(c), Found: []string{}}
	v.Delegation, v.Found = p.delegation(ctx, zone, ns)
	plugin.WriteJSON(w, 200, v)
}

// delegation looks up who the world thinks serves a zone.
func (p *Plugin) delegation(ctx context.Context, zone string, ours []string) (string, []string) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	found, err := p.LookupNS(ctx, zone)
	if found == nil {
		found = []string{}
	}
	var de *net.DNSError
	switch {
	case errors.As(err, &de) && de.IsNotFound:
		return "none", found
	case err != nil:
		return "unknown", found
	}
	want := map[string]bool{}
	for _, n := range ours {
		want[n] = true
	}
	for _, f := range found {
		if !want[f] {
			return "other", found
		}
	}
	if len(found) == 0 {
		return "none", found
	}
	return "ok", found
}

func (p *Plugin) putRecords(w http.ResponseWriter, r *http.Request) {
	zone, _, c, ok := p.mayUse(w, r)
	if !ok {
		return
	}
	var in struct {
		Set
		// Old is the set being edited when its name or type changes.
		Old *Set `json:"old"`
	}
	if err := plugin.ReadJSON(r, &in); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	set, err := buildSet(zone, in.Set, ttlOf(c))
	if err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	sets := []RRSet{set}
	if in.Old != nil {
		old, err := deleteSet(zone, *in.Old)
		if err != nil {
			plugin.WriteErr(w, 400, err.Error())
			return
		}
		if old.Name != set.Name || old.Type != set.Type {
			sets = append([]RRSet{old}, sets...)
		}
	}
	if err := p.patch(r.Context(), c, zone, sets); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	plugin.WriteJSON(w, 200, map[string]any{"ok": true})
}

func deleteSet(zone string, s Set) (RRSet, error) {
	typ := strings.ToUpper(s.Type)
	name, err := absName(s.Name, zone)
	if err != nil {
		return RRSet{}, err
	}
	if !editable[typ] || typ == "NS" && name == zone {
		return RRSet{}, fmt.Errorf("%s records of the zone itself are managed by Berth", typ)
	}
	return RRSet{Name: fqdn(name), Type: typ, ChangeType: "DELETE", Records: []Record{}}, nil
}

func (p *Plugin) deleteRecords(w http.ResponseWriter, r *http.Request) {
	zone, _, c, ok := p.mayUse(w, r)
	if !ok {
		return
	}
	var in Set
	if err := plugin.ReadJSON(r, &in); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	set, err := deleteSet(zone, in)
	if err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	if err := p.patch(r.Context(), c, zone, []RRSet{set}); err != nil {
		plugin.WriteErr(w, 400, err.Error())
		return
	}
	plugin.WriteJSON(w, 200, map[string]any{"ok": true})
}

// patch changes records on the primary and tells the secondaries.
func (p *Plugin) patch(ctx context.Context, c plugin.Config, zone string, sets []RRSet) error {
	pd := p.primary(c)
	if err := pd.Patch(ctx, zone, sets); err != nil {
		return err
	}
	_ = pd.Notify(ctx, zone)
	return nil
}

func (p *Plugin) deleteZone(w http.ResponseWriter, r *http.Request) {
	zone, o, c, ok := p.mayUse(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.primary(c).DeleteZone(ctx, zone); err != nil {
		plugin.WriteErr(w, 502, err.Error())
		return
	}
	warn := ""
	if s := p.secondary(c); s != nil {
		if err := s.DeleteZone(ctx, zone); err != nil {
			warn = "The secondary could not remove it: " + err.Error()
		}
	}
	owners := p.owners(ctx)
	delete(owners, zone)
	if err := p.host.SetData(ctx, "zones", owners); err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	_ = o
	p.host.Event(ctx, "warn", fmt.Sprintf("%s deleted DNS zone %s", plugin.User(r).Username, zone))
	plugin.WriteJSON(w, 200, map[string]any{"ok": true, "warning": warn})
}

// moveZone gives a zone to another account (admins only).
func (p *Plugin) moveZone(w http.ResponseWriter, r *http.Request) {
	if !plugin.User(r).IsAdmin() {
		plugin.WriteErr(w, 403, "only admins can move zones")
		return
	}
	zone, _, _, ok := p.mayUse(w, r)
	if !ok {
		return
	}
	var in struct {
		Account string `json:"account_id"`
	}
	if err := plugin.ReadJSON(r, &in); err != nil || in.Account == "" {
		plugin.WriteErr(w, 400, "choose an account")
		return
	}
	ctx := r.Context()
	p.mu.Lock()
	defer p.mu.Unlock()
	owners := p.owners(ctx)
	o := owners[zone]
	if o.Created.IsZero() {
		o.Created = time.Now().UTC()
	}
	o.Account = in.Account
	owners[zone] = o
	if err := p.host.SetData(ctx, "zones", owners); err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	plugin.WriteJSON(w, 200, map[string]any{"ok": true})
}

// sync makes the secondary hold exactly the primary's zones (admins only).
func (p *Plugin) sync(w http.ResponseWriter, r *http.Request) {
	if !plugin.User(r).IsAdmin() {
		plugin.WriteErr(w, 403, "only admins can do that")
		return
	}
	ctx := r.Context()
	c, err := p.config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	added, removed, err := p.Sync(ctx, c)
	if err != nil {
		plugin.WriteErr(w, 502, err.Error())
		return
	}
	plugin.WriteJSON(w, 200, map[string]any{"added": added, "removed": removed})
}

// Sync adds the primary's zones to the secondary and removes the secondary's
// zones that came from the primary but are gone there.
func (p *Plugin) Sync(ctx context.Context, c plugin.Config) (added, removed int, err error) {
	s := p.secondary(c)
	if s == nil {
		return 0, 0, errors.New("no secondary is set up")
	}
	prim, err := p.primary(c).Zones(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("primary: %w", err)
	}
	sec, err := s.Zones(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("secondary: %w", err)
	}
	have := map[string]Zone{}
	for _, z := range sec {
		have[z.Name] = z
	}
	want := map[string]bool{}
	var errs []string
	for _, z := range prim {
		want[z.Name] = true
		if _, ok := have[z.Name]; ok {
			continue
		}
		if err := p.addSecondary(ctx, c, z.Name); err != nil {
			errs = append(errs, z.Name+": "+err.Error())
			continue
		}
		added++
	}
	for _, z := range sec {
		if want[z.Name] || !strings.EqualFold(z.Kind, "Slave") || !fromPrimary(z, c.Str("primary_ip")) {
			continue
		}
		if err := s.DeleteZone(ctx, z.Name); err != nil {
			errs = append(errs, z.Name+": "+err.Error())
			continue
		}
		removed++
	}
	if len(errs) > 0 {
		return added, removed, errors.New(strings.Join(errs, "; "))
	}
	return added, removed, nil
}

func fromPrimary(z Zone, ip string) bool {
	for _, m := range z.Masters {
		if m == ip || strings.HasPrefix(m, ip+":") {
			return true
		}
	}
	return false
}

type hostStatus struct {
	Name   string   `json:"name"`
	Found  []string `json:"found"`
	Points bool     `json:"points"` // resolves to this server
}

type siteDNS struct {
	Zone        string       `json:"zone"` // the account's zone holding the site, if any
	Delegation  string       `json:"delegation,omitempty"`
	Nameservers []string     `json:"nameservers"`
	IPv4        string       `json:"ipv4"`
	IPv6        string       `json:"ipv6"`
	Hosts       []hostStatus `json:"hosts"`
}

// siteStatus tells a site's owner whether its domains reach this server, and
// how to make them.
func (p *Plugin) siteStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := plugin.User(r)
	s, err := p.host.Site(ctx, r.PathValue("name"))
	if err != nil || !u.IsAdmin() && s.AccountID != u.AccountID {
		plugin.WriteErr(w, 404, "no such site")
		return
	}
	c, err := p.config(ctx)
	if err != nil {
		plugin.WriteErr(w, 500, err.Error())
		return
	}
	ns, _ := nameservers(c)
	if ns == nil {
		ns = []string{}
	}
	out := siteDNS{Nameservers: ns, IPv4: c.Str("ipv4"), IPv6: c.Str("ipv6"), Hosts: []hostStatus{}}
	owners := p.owners(ctx)
	out.Zone = zoneFor(owners, s.AccountID, s.Domain)
	if out.Zone != "" {
		out.Delegation, _ = p.delegation(ctx, out.Zone, ns)
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	out.Hosts = make([]hostStatus, len(s.Hostnames()))
	for i, h := range s.Hostnames() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found, _ := p.LookupIP(lctx, h)
			if found == nil {
				found = []string{}
			}
			hs := hostStatus{Name: h, Found: found}
			for _, f := range found {
				if f == out.IPv4 || f == out.IPv6 {
					hs.Points = true
				}
			}
			out.Hosts[i] = hs
		}()
	}
	wg.Wait()
	plugin.WriteJSON(w, 200, out)
}

// SiteChanged implements plugin.SiteWatcher: names a site gains get A/AAAA
// records in the account's zone, and names it loses have them removed.
func (p *Plugin) SiteChanged(ctx context.Context, old, cur *model.Site, logf func(string, ...any)) {
	c, err := p.config(ctx)
	if err != nil {
		return
	}
	if old == nil && cur != nil {
		p.autoZone(ctx, c, cur, logf)
	}
	if !c.Bool("auto_records") || c.Str("ipv4") == "" && c.Str("ipv6") == "" {
		return
	}
	owners := p.owners(ctx)
	if len(owners) == 0 {
		return
	}
	hosts := func(s *model.Site) map[string]bool {
		m := map[string]bool{}
		if s != nil && p.host.ActiveFor(ctx, s.AccountID) {
			for _, h := range s.Hostnames() {
				m[h] = true
			}
		}
		return m
	}
	before, after := hosts(old), hosts(cur)
	sameAccount := old != nil && cur != nil && old.AccountID == cur.AccountID
	pd := p.primary(c)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Remove what pointed here for names the site no longer has.
	if old != nil {
		for h := range before {
			if sameAccount && after[h] {
				continue
			}
			zone := zoneFor(owners, old.AccountID, h)
			if zone == "" {
				continue
			}
			z, err := pd.Zone(ctx, zone)
			if err != nil {
				logf("DNS: could not read zone %s: %v", zone, err)
				continue
			}
			var del []RRSet
			for _, set := range z.RRSets {
				if set.Name == fqdn(h) && (set.Type == "A" && only(set, c.Str("ipv4")) || set.Type == "AAAA" && only(set, c.Str("ipv6"))) {
					del = append(del, RRSet{Name: set.Name, Type: set.Type, ChangeType: "DELETE", Records: []Record{}})
				}
			}
			if len(del) == 0 {
				continue
			}
			if err := p.patch(ctx, c, zone, del); err != nil {
				logf("DNS: could not remove the records for %s: %v", h, err)
				continue
			}
			logf("DNS: removed the records for %s", h)
			p.host.SiteEvent(ctx, "info", old.Name, "Removed the DNS records for "+h)
		}
	}
	if cur == nil {
		return
	}
	byZone := map[string][]string{}
	for h := range after {
		if sameAccount && before[h] {
			continue
		}
		if zone := zoneFor(owners, cur.AccountID, h); zone != "" {
			byZone[zone] = append(byZone[zone], h)
		}
	}
	for zone, hs := range byZone {
		z, err := pd.Zone(ctx, zone)
		if err != nil {
			logf("DNS: could not read zone %s: %v", zone, err)
			continue
		}
		taken := map[string]bool{}
		for _, set := range z.RRSets {
			if set.Type == "A" || set.Type == "AAAA" || set.Type == "CNAME" {
				taken[strings.TrimSuffix(set.Name, ".")] = true
			}
		}
		sort.Strings(hs)
		var add []RRSet
		var names []string
		for _, h := range hs {
			if taken[h] {
				logf("DNS: %s already has a record in %s; left as it is", h, zone)
				continue
			}
			add = append(add, addrSets(c, h)...)
			names = append(names, h)
		}
		if len(add) == 0 {
			continue
		}
		if err := p.patch(ctx, c, zone, add); err != nil {
			logf("DNS: could not add records to %s: %v", zone, err)
			p.host.SiteEvent(ctx, "warn", cur.Name, "Could not add DNS records: "+err.Error())
			continue
		}
		logf("DNS: pointed %s at this server", strings.Join(names, ", "))
		p.host.SiteEvent(ctx, "info", cur.Name, "Pointed "+strings.Join(names, ", ")+" at this server in DNS")
	}
}

func only(set RRSet, ip string) bool {
	return ip != "" && len(set.Records) == 1 && set.Records[0].Content == ip
}

// autoZone adds a new customer site's domain as a zone, so the customer
// only has to set the nameservers at their registrar. It skips the admin's
// own sites, domains already hosted here and accounts at
// their plan's limit.
func (p *Plugin) autoZone(ctx context.Context, c plugin.Config, s *model.Site, logf func(string, ...any)) {
	if !c.Bool("auto_zones") || s.AccountID == "" || s.AccountID == p.host.AdminAccount(ctx) || !p.host.ActiveFor(ctx, s.AccountID) {
		return
	}
	if zoneFor(p.owners(ctx), s.AccountID, s.Domain) != "" {
		return
	}
	apex, err := publicsuffix.EffectiveTLDPlusOne(s.Domain)
	if err != nil {
		return
	}
	zone, err := CheckZoneName(apex)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	warn, err := p.addZone(ctx, c, s.AccountID, zone, false)
	if err != nil {
		logf("DNS: did not add %s as a zone: %v", zone, err)
		return
	}
	if warn != "" {
		logf("DNS: %s", warn)
	}
	logf("DNS: added %s; set its nameservers to %s at the registrar", zone, c.Str("nameservers"))
	p.host.SiteEvent(ctx, "info", s.Name, "Added "+zone+" to DNS. Set its nameservers at the registrar to point it here.")
}
