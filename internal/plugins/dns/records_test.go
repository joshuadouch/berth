package dns

import (
	"strings"
	"testing"

	"github.com/joshuadouch/berth/internal/plugin"
)

func TestCheckZoneName(t *testing.T) {
	for in, want := range map[string]string{"Example.COM.": "example.com", "shop.example.co.uk": "shop.example.co.uk", "example.co.uk": "example.co.uk"} {
		if got, err := CheckZoneName(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"com", "co.uk", "localhost", "a b.com", "-x.com", "*.example.com", ""} {
		if _, err := CheckZoneName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNormContent(t *testing.T) {
	ok := map[[2]string]string{
		{"A", " 192.0.2.1 "}:               "192.0.2.1",
		{"AAAA", "2001:DB8::1"}:            "2001:db8::1",
		{"CNAME", "Target.Example.org"}:    "target.example.org.",
		{"MX", "10 mail.example.org."}:     "10 mail.example.org.",
		{"SRV", "0 5 443 svc.example.org"}: "0 5 443 svc.example.org.",
		{"CAA", "0 issue letsencrypt.org"}: `0 issue "letsencrypt.org"`,
		{"CAA", `0 iodef "mailto:a@b.c"`}:  `0 iodef "mailto:a@b.c"`,
		{"TXT", `v=spf1 "mx" ~all`}:        `"v=spf1 \"mx\" ~all"`,
		{"TXT", `"already" "quoted"`}:      `"already" "quoted"`,
		{"NS", "ns1.elsewhere.net"}:        "ns1.elsewhere.net.",
	}
	for in, want := range ok {
		if got, err := normContent(in[0], in[1]); err != nil || got != want {
			t.Errorf("%v: %q %v", in, got, err)
		}
	}
	for _, in := range [][2]string{{"A", "::1"}, {"AAAA", "1.2.3.4"}, {"MX", "mail.example.org"}, {"SRV", "1 2 x y"}, {"SOA", "x"}, {"CNAME", "bad name"}, {"TXT", " "}} {
		if _, err := normContent(in[0], in[1]); err == nil {
			t.Errorf("%v accepted", in)
		}
	}
	long := strings.Repeat("k", 300)
	if got, _ := normContent("TXT", long); got != `"`+long[:255]+`" "`+long[255:]+`"` {
		t.Errorf("long TXT not split: %s", got)
	}
}

func TestBuildSet(t *testing.T) {
	s, err := buildSet("example.com", Set{Name: "www", Type: "a", Records: []string{"192.0.2.1", "192.0.2.1", ""}}, 3600)
	if err != nil || s.Name != "www.example.com." || s.Type != "A" || len(s.Records) != 1 || s.TTL != 3600 {
		t.Fatal(s, err)
	}
	if s, _ := buildSet("example.com", Set{Name: "www.example.com.", Type: "A", TTL: 300, Records: []string{"192.0.2.1"}}, 3600); s.Name != "www.example.com." || s.TTL != 300 {
		t.Fatal(s)
	}
	for _, bad := range []Set{
		{Name: "@", Type: "NS", Records: []string{"ns.x.net"}},
		{Name: "@", Type: "CNAME", Records: []string{"x.net"}},
		{Name: "a", Type: "A"},
		{Name: "a", Type: "A", TTL: 5, Records: []string{"192.0.2.1"}},
		{Name: "a..b", Type: "A", Records: []string{"192.0.2.1"}},
	} {
		if _, err := buildSet("example.com", bad, 3600); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if s, err := buildSet("example.com", Set{Name: "*", Type: "A", Records: []string{"192.0.2.1"}}, 3600); err != nil || s.Name != "*.example.com." {
		t.Error("wildcard", s, err)
	}
}

func TestToSets(t *testing.T) {
	sets := toSets("example.com.", []RRSet{
		{Name: "www.example.com.", Type: "A", Records: []Record{{Content: "192.0.2.1"}}},
		{Name: "example.com.", Type: "NS", Records: []Record{{Content: "b."}, {Content: "a."}}},
		{Name: "example.com.", Type: "SOA", Records: []Record{{Content: "x"}}},
		{Name: "a.www.example.com.", Type: "TXT", Records: []Record{{Content: `"x"`}}},
		{Name: "example.com.", Type: "A", Records: []Record{{Content: "192.0.2.1"}}},
	})
	var got []string
	for _, s := range sets {
		got = append(got, s.Name+"/"+s.Type)
	}
	if strings.Join(got, " ") != "@/SOA @/NS @/A www/A a.www/TXT" || !sets[0].Locked || !sets[1].Locked || sets[2].Locked || sets[1].Records[0] != "a." {
		t.Fatal(got, sets)
	}
}

func TestSOAAndValidate(t *testing.T) {
	c := plugin.Config{"hostmaster": "dns.admin@example.net"}
	if got := soa(c, "x.com", []string{"ns1.example.net"}); got != `ns1.example.net. dns\.admin.example.net. 1 10800 3600 1209600 3600` {
		t.Error(got)
	}
	if got := soa(plugin.Config{}, "x.com", []string{"ns1.example.net"}); !strings.HasPrefix(got, "ns1.example.net. hostmaster.x.com. ") {
		t.Error(got)
	}
	p := New()
	if err := p.Validate(plugin.Config{"nameservers": "ns1.example.net, ns2.example.net", "ipv4": "203.0.113.1", "primary_ip": "10.0.0.1:5300"}); err != nil {
		t.Error(err)
	}
	for _, bad := range []plugin.Config{
		{"nameservers": ""},
		{"nameservers": "ns1"},
		{"nameservers": "ns1.x.net", "ipv4": "2001:db8::1"},
		{"nameservers": "ns1.x.net", "secondary_url": "http://x"},
	} {
		if err := p.Validate(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestZoneFor(t *testing.T) {
	o := map[string]Owner{"example.com": {Account: "a"}, "shop.example.com": {Account: "a"}, "other.com": {Account: "b"}}
	for host, want := range map[string]string{"example.com": "example.com", "www.example.com": "example.com", "x.shop.example.com": "shop.example.com", "other.com": "", "notexample.com": ""} {
		if got := zoneFor(o, "a", host); got != want {
			t.Errorf("%s: %q", host, got)
		}
	}
}
