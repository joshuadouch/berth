package dns

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Types customers may edit. SOA and the zone's own NS records are Berth's.
var editable = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "SRV": true, "CAA": true, "NS": true}

var labelRe = regexp.MustCompile(`^(\*|_?[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?)$`)

// validName reports whether a name (no trailing dot) is a usable DNS name.
func validName(name string, wildcard bool) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for i, l := range strings.Split(name, ".") {
		if l == "*" && (!wildcard || i != 0) || !labelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// CheckZoneName says whether a zone name may be hosted: a real domain, not a
// public suffix such as com or co.uk.
func CheckZoneName(name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if !validName(name, false) || !strings.Contains(name, ".") {
		return "", fmt.Errorf("%q is not a domain name", name)
	}
	if etld, icann := publicsuffix.PublicSuffix(name); etld == name && icann {
		return "", fmt.Errorf("%s is a public suffix: add the domain you registered under it", name)
	}
	return name, nil
}

// absName turns a name typed in the zone editor ("@", "www", or a full name)
// into the full name without the trailing dot.
func absName(name, zone string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	switch {
	case name == "" || name == "@":
		name = zone
	case name == zone || strings.HasSuffix(name, "."+zone):
	default:
		name += "." + zone
	}
	if !validName(name, true) {
		return "", fmt.Errorf("%q is not a valid name", name)
	}
	return name, nil
}

// relName shows a full name relative to the zone ("@" for the apex).
func relName(name, zone string) string {
	name = strings.TrimSuffix(name, ".")
	zone = strings.TrimSuffix(zone, ".")
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

func target(s string) (string, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if s != "" && !validName(s, false) {
		return "", fmt.Errorf("%q is not a host name", s)
	}
	return s + ".", nil
}

// quoteTXT puts text in the quoted strings a TXT record holds, split into
// 255-byte pieces. Text that is already quoted is kept as typed.
func quoteTXT(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) > 1 {
		return s
	}
	var parts []string
	for len(s) > 255 {
		parts = append(parts, s[:255])
		s = s[255:]
	}
	parts = append(parts, s)
	for i, p := range parts {
		p = strings.ReplaceAll(p, `\`, `\\`)
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
	}
	return strings.Join(parts, " ")
}

// normContent checks one record's content and writes it the way PowerDNS
// wants it.
func normContent(typ, c string) (string, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return "", errors.New("a record needs a value")
	}
	f := strings.Fields(c)
	switch typ {
	case "A":
		a, err := netip.ParseAddr(c)
		if err != nil || !a.Is4() {
			return "", fmt.Errorf("%q is not an IPv4 address", c)
		}
		return a.String(), nil
	case "AAAA":
		a, err := netip.ParseAddr(c)
		if err != nil || !a.Is6() || a.Is4In6() {
			return "", fmt.Errorf("%q is not an IPv6 address", c)
		}
		return a.String(), nil
	case "CNAME", "NS":
		return target(c)
	case "MX":
		if len(f) != 2 {
			return "", errors.New(`MX is "priority host", like "10 mail.example.com"`)
		}
		if _, err := strconv.ParseUint(f[0], 10, 16); err != nil {
			return "", fmt.Errorf("MX priority %q is not a number", f[0])
		}
		t, err := target(f[1])
		return f[0] + " " + t, err
	case "SRV":
		if len(f) != 4 {
			return "", errors.New(`SRV is "priority weight port target", like "10 5 5060 sip.example.com"`)
		}
		for _, n := range f[:3] {
			if _, err := strconv.ParseUint(n, 10, 16); err != nil {
				return "", fmt.Errorf("SRV field %q is not a number", n)
			}
		}
		t, err := target(f[3])
		return strings.Join(f[:3], " ") + " " + t, err
	case "CAA":
		if len(f) < 3 {
			return "", errors.New(`CAA is "flags tag value", like "0 issue letsencrypt.org"`)
		}
		if _, err := strconv.ParseUint(f[0], 10, 8); err != nil {
			return "", fmt.Errorf("CAA flags %q is not a number", f[0])
		}
		rest := strings.TrimSpace(c[len(f[0]):])
		v := strings.TrimSpace(rest[len(f[1]):])
		if !strings.HasPrefix(v, `"`) {
			v = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
		}
		return f[0] + " " + strings.ToLower(f[1]) + " " + v, nil
	case "TXT":
		return quoteTXT(c), nil
	}
	return "", fmt.Errorf("%s records cannot be edited here", typ)
}

// Set is one name and type in the zone editor.
type Set struct {
	Name    string   `json:"name"` // relative to the zone, "@" for the apex
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Records []string `json:"records"`
	// Locked sets are Berth's (SOA, the zone's NS records).
	Locked bool `json:"locked,omitempty"`
}

// toSets turns PowerDNS RRSets into the editor's view, sorted by name.
func toSets(zone string, in []RRSet) []Set {
	out := []Set{}
	for _, r := range in {
		s := Set{Name: relName(r.Name, zone), Type: r.Type, TTL: r.TTL, Records: []string{}}
		for _, rec := range r.Records {
			s.Records = append(s.Records, rec.Content)
		}
		sort.Strings(s.Records)
		s.Locked = r.Type == "SOA" || r.Type == "NS" && s.Name == "@" || !editable[r.Type]
		out = append(out, s)
	}
	rank := func(s Set) string {
		n := s.Name
		if n == "@" {
			n = ""
		}
		// Sort by the name read right to left, so subdomains group together.
		l := strings.Split(n, ".")
		for i, j := 0, len(l)-1; i < j; i, j = i+1, j-1 {
			l[i], l[j] = l[j], l[i]
		}
		return strings.Join(l, ".")
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := rank(out[i]), rank(out[j])
		if a != b {
			return a < b
		}
		order := map[string]int{"SOA": 0, "NS": 1}
		oa, ok := order[out[i].Type]
		if !ok {
			oa = 2
		}
		ob, ok := order[out[j].Type]
		if !ok {
			ob = 2
		}
		if oa != ob {
			return oa < ob
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// buildSet checks a set from the editor and turns it into a PowerDNS RRSet.
func buildSet(zone string, s Set, ttl int) (RRSet, error) {
	typ := strings.ToUpper(strings.TrimSpace(s.Type))
	if !editable[typ] {
		return RRSet{}, fmt.Errorf("%s records cannot be edited here", typ)
	}
	name, err := absName(s.Name, zone)
	if err != nil {
		return RRSet{}, err
	}
	if name == zone && (typ == "NS" || typ == "CNAME") {
		return RRSet{}, fmt.Errorf("the zone itself cannot have its own %s records here", typ)
	}
	if s.TTL > 0 {
		ttl = s.TTL
	}
	if ttl < 60 || ttl > 604800 {
		return RRSet{}, errors.New("TTL must be between 60 seconds and a week")
	}
	set := RRSet{Name: fqdn(name), Type: typ, TTL: ttl, ChangeType: "REPLACE", Records: []Record{}}
	seen := map[string]bool{}
	for _, c := range s.Records {
		if strings.TrimSpace(c) == "" {
			continue
		}
		v, err := normContent(typ, c)
		if err != nil {
			return RRSet{}, err
		}
		if !seen[v] {
			seen[v] = true
			set.Records = append(set.Records, Record{Content: v})
		}
	}
	if len(set.Records) == 0 {
		return RRSet{}, errors.New("add at least one value")
	}
	if typ == "CNAME" && len(set.Records) > 1 {
		return RRSet{}, errors.New("a name can only have one CNAME")
	}
	return set, nil
}
