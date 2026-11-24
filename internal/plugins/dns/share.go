package dns

import (
	"context"
	"strings"
)

// These methods let other plugins (mailcow) put records into the zones that
// customers host here.

// ZoneOf returns the zone account owns that holds host, or "" when none does.
func (p *Plugin) ZoneOf(ctx context.Context, account, host string) string {
	if p.host == nil {
		return ""
	}
	return zoneFor(p.owners(ctx), account, strings.TrimSuffix(strings.ToLower(host), "."))
}

// Records returns the contents of the records of one type at a name, as
// PowerDNS writes them (MX "10 mail.example.com.", TXT quoted).
func (p *Plugin) Records(ctx context.Context, zone, typ, name string) ([]string, error) {
	c, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	z, err := p.primary(c).Zone(ctx, zone)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, set := range z.RRSets {
		if strings.EqualFold(set.Name, fqdn(name)) && strings.EqualFold(set.Type, typ) {
			for _, r := range set.Records {
				if !r.Disabled {
					out = append(out, r.Content)
				}
			}
		}
	}
	return out, nil
}

// AddRecord adds one record at a name, keeping the records of that type
// already there. content is in PowerDNS form.
func (p *Plugin) AddRecord(ctx context.Context, zone, typ, name, content string) error {
	c, err := p.config(ctx)
	if err != nil {
		return err
	}
	z, err := p.primary(c).Zone(ctx, zone)
	if err != nil {
		return err
	}
	set := RRSet{Name: fqdn(name), Type: strings.ToUpper(typ), TTL: ttlOf(c), ChangeType: "REPLACE"}
	for _, s := range z.RRSets {
		if strings.EqualFold(s.Name, set.Name) && strings.EqualFold(s.Type, set.Type) {
			set.Records = append(set.Records, s.Records...)
			if s.TTL > 0 {
				set.TTL = s.TTL
			}
		}
	}
	set.Records = append(set.Records, Record{Content: content})
	return p.patch(ctx, c, zone, []RRSet{set})
}
