package mailcow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/store"
)

// fakeMailcow is just enough of the mailcow API.
type fakeMailcow struct {
	mu         sync.Mutex
	domains    map[string]map[string]any
	mailboxes  []map[string]any
	aliases    []map[string]any
	dkim       map[string]string
	relayhosts []map[string]any
	calls      []string
}

func newFakeMailcow() *fakeMailcow {
	return &fakeMailcow{domains: map[string]map[string]any{}, dkim: map[string]string{}}
}

func (f *fakeMailcow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-API-Key") != "key" {
		w.WriteHeader(401)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	f.calls = append(f.calls, r.Method+" "+p)
	var body map[string]any
	var list []any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	_ = json.Unmarshal(b, &list)
	ok := []map[string]any{{"type": "success", "msg": "ok"}}
	out := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case p == "get/status/version":
		out(map[string]string{"version": "2025-10"})
	case p == "get/domain/all":
		var l []map[string]any
		for _, d := range f.domains {
			l = append(l, d)
		}
		out(l)
	case strings.HasPrefix(p, "get/domain/"):
		if d, ok := f.domains[strings.TrimPrefix(p, "get/domain/")]; ok {
			out(d)
		} else {
			out([]any{})
		}
	case p == "add/domain":
		name := body["domain"].(string)
		if _, exists := f.domains[name]; exists {
			out([]map[string]any{{"type": "danger", "msg": []string{"domain_exists", name}}})
			return
		}
		f.domains[name] = map[string]any{"domain_name": name, "active": "1", "relayhost": "0", "max_quota_for_domain": 10240 << 20, "mboxes_in_domain": 0}
		out(ok)
	case p == "edit/domain":
		attr := body["attr"].(map[string]any)
		for _, it := range body["items"].([]any) {
			for k, v := range attr {
				f.domains[it.(string)][k] = v
			}
		}
		out(ok)
	case p == "delete/domain":
		for _, it := range list {
			delete(f.domains, it.(string))
		}
		out(ok)
	case strings.HasPrefix(p, "get/dkim/"):
		d := strings.TrimPrefix(p, "get/dkim/")
		if txt, ok := f.dkim[d]; ok {
			out(map[string]any{"dkim_selector": "dkim", "dkim_txt": txt})
		} else {
			out([]any{})
		}
	case p == "add/dkim":
		f.dkim[body["domains"].(string)] = "v=DKIM1;k=rsa;t=s;s=email;p=" + strings.Repeat("A", 380)
		out(ok)
	case p == "get/relayhost/all":
		out(f.relayhosts)
	case p == "add/relayhost":
		body["id"] = fmt.Sprint(len(f.relayhosts) + 7)
		body["active"] = "1"
		f.relayhosts = append(f.relayhosts, body)
		out(ok)
	case p == "get/mailbox/all":
		out(f.mailboxes)
	case p == "add/mailbox":
		if body["password"] != body["password2"] || body["password"] == "" {
			out([]map[string]any{{"type": "danger", "msg": "password_mismatch"}})
			return
		}
		f.mailboxes = append(f.mailboxes, map[string]any{"username": body["local_part"].(string) + "@" + body["domain"].(string), "domain": body["domain"], "local_part": body["local_part"], "name": body["name"], "quota": 1 << 30, "active": "1"})
		out(ok)
	case p == "get/alias/all":
		out(f.aliases)
	case p == "add/alias":
		f.aliases = append(f.aliases, map[string]any{"id": len(f.aliases) + 1, "address": body["address"], "goto": body["goto"], "domain": strings.SplitN(body["address"].(string), "@", 2)[1], "active": 1})
		out(ok)
	case p == "delete/alias":
		out(ok)
	default:
		w.WriteHeader(404)
	}
}

// fakeSES keeps identities; verified ones are listed in verified.
type fakeSES struct {
	mu       sync.Mutex
	ids      map[string]map[string]any
	verified map[string]bool
	auth     []string
}

func (f *fakeSES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/") {
		w.WriteHeader(403)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v2/email/")
	notFound := func() {
		w.Header().Set("X-Amzn-ErrorType", "NotFoundException:http://internal.amazon.com/coral/com.amazonaws.sesv2/")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Email identity does not exist."}`))
	}
	switch {
	case p == "account":
		_ = json.NewEncoder(w).Encode(map[string]any{"SendingEnabled": true, "ProductionAccessEnabled": false})
	case p == "identities" && r.Method == "POST":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		d := b["EmailIdentity"].(string)
		f.ids[d] = map[string]any{"MailFromAttributes": map[string]any{}}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	case strings.HasSuffix(p, "/mail-from") && r.Method == "PUT":
		d := strings.TrimSuffix(strings.TrimPrefix(p, "identities/"), "/mail-from")
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.ids[d]["MailFromAttributes"] = map[string]any{"MailFromDomain": b["MailFromDomain"], "MailFromDomainStatus": "PENDING"}
	case strings.HasPrefix(p, "identities/"):
		d := strings.TrimPrefix(p, "identities/")
		id, ok := f.ids[d]
		if !ok {
			notFound()
			return
		}
		if r.Method == "DELETE" {
			delete(f.ids, d)
			return
		}
		st := "PENDING"
		if f.verified[d] {
			st = "SUCCESS"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"VerifiedForSendingStatus": f.verified[d], "DkimAttributes": map[string]any{"Status": st, "Tokens": []string{"tok1", "tok2", "tok3"}}, "MailFromAttributes": id["MailFromAttributes"]})
	default:
		w.WriteHeader(404)
	}
}

// fakeDNS is an in-memory record set that can be written.
type fakeDNS struct {
	mu   sync.Mutex
	recs map[string][]string // "TYPE name" → contents
}

func (f *fakeDNS) Lookup(_ context.Context, typ, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recs[typ+" "+name], nil
}

func (f *fakeDNS) Create(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := r.Content
	if r.Type == "MX" {
		c = fmt.Sprintf("%d %s", r.Priority, r.Content)
	}
	f.recs[r.Type+" "+r.Name] = append(f.recs[r.Type+" "+r.Name], c)
	return nil
}

type rig struct {
	t   *testing.T
	p   *Plugin
	reg *plugin.Registry
	mc  *fakeMailcow
	ses *fakeSES
	dns *fakeDNS
	h   http.Handler
}

func newRig(t *testing.T) *rig {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mc, ses := newFakeMailcow(), &fakeSES{ids: map[string]map[string]any{}, verified: map[string]bool{}}
	mcs, sess := httptest.NewServer(mc), httptest.NewServer(ses)
	t.Cleanup(mcs.Close)
	t.Cleanup(sess.Close)
	dns := &fakeDNS{recs: map[string][]string{
		"TXT example.com": {"google-site-verification=abc", "v=spf1 include:_spf.google.com ~all"},
	}}
	p := New()
	p.sesBase, p.dns = sess.URL, dns
	reg := plugin.NewRegistry(st, nil, p)
	_, err = reg.Save(context.Background(), "mailcow", true, plugin.Config{
		"url": mcs.URL, "api_key": "key", "mail_host": "mail.host.net",
		"ses_region": "eu-west-2", "ses_access_key": "AKID", "ses_secret_key": "secret", "cf_token": "cf",
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/p/{id}/", reg)
	return &rig{t: t, p: p, reg: reg, mc: mc, ses: ses, dns: dns, h: mux}
}

func (r *rig) do(method, path string, body any) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/api/p/mailcow"+path, &buf)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func steps(out map[string]any) map[string]string {
	m := map[string]string{}
	for _, s := range out["steps"].([]any) {
		s := s.(map[string]any)
		m[s["name"].(string)] = s["status"].(string)
	}
	return m
}

func TestAddDomainSetsUpSESAndDNS(t *testing.T) {
	r := newRig(t)
	code, out := r.do("POST", "/domains", map[string]any{"domain": "Example.com"})
	if code != 200 {
		t.Fatal(code, out)
	}
	st := steps(out)
	if st["mailcow domain"] != "ok" || st["mailcow DKIM key"] != "ok" || st["Amazon SES identity"] != "ok" || st["SES MAIL FROM"] != "ok" {
		t.Fatalf("steps: %v", out["steps"])
	}
	// Not yet verified: the transport waits.
	if st["SES transport"] != "warn" || r.mc.domains["example.com"]["relayhost"] != "0" {
		t.Fatalf("transport should wait for verification: %v %v", st, r.mc.domains["example.com"])
	}
	if _, ok := r.p.pending(context.Background())["example.com"]; !ok {
		t.Fatal("domain should be pending")
	}
	// DNS: everything created except SPF, which clashes with Google's.
	statuses := map[string]string{}
	for _, rec := range out["records"].([]any) {
		rec := rec.(map[string]any)
		statuses[rec["type"].(string)+" "+rec["name"].(string)] = rec["status"].(string)
	}
	want := map[string]string{
		"MX example.com":                    "created",
		"TXT example.com":                   "conflict",
		"TXT dkim._domainkey.example.com":   "created",
		"CNAME tok1._domainkey.example.com": "created",
		"MX bounce.example.com":             "created",
		"TXT bounce.example.com":            "created",
		"TXT _dmarc.example.com":            "created",
		"CNAME autodiscover.example.com":    "created",
		"CNAME autoconfig.example.com":      "created",
		"CNAME tok3._domainkey.example.com": "created",
	}
	for k, v := range want {
		if statuses[k] != v {
			t.Errorf("%s: got %q want %q", k, statuses[k], v)
		}
	}
	if got := r.dns.recs["MX bounce.example.com"]; len(got) != 1 || got[0] != "10 feedback-smtp.eu-west-2.amazonses.com" {
		t.Errorf("MAIL FROM MX: %v", got)
	}
	if r.dns.recs["TXT example.com"][1] != "v=spf1 include:_spf.google.com ~all" || len(r.dns.recs["TXT example.com"]) != 2 {
		t.Error("existing SPF must be left alone")
	}

	// SES verifies; the watcher switches the transport on.
	r.ses.verified["example.com"] = true
	r.p.checkPending(context.Background())
	if len(r.mc.relayhosts) != 1 {
		t.Fatalf("relayhosts: %v", r.mc.relayhosts)
	}
	rh := r.mc.relayhosts[0]
	if rh["hostname"] != "email-smtp.eu-west-2.amazonaws.com:587" || rh["username"] != "AKID" || rh["password"] != SMTPPassword("secret", "eu-west-2") {
		t.Fatalf("relayhost: %v", rh)
	}
	if r.mc.domains["example.com"]["relayhost"] != rh["id"] {
		t.Fatalf("domain relayhost: %v", r.mc.domains["example.com"]["relayhost"])
	}
	if len(r.p.pending(context.Background())) != 0 {
		t.Fatal("pending should be cleared")
	}

	// A second domain that SES already verified gets the same transport at once.
	r.ses.ids["two.org"] = map[string]any{"MailFromAttributes": map[string]any{"MailFromDomain": "bounce.two.org"}}
	r.ses.verified["two.org"] = true
	code, out = r.do("POST", "/domains", map[string]any{"domain": "two.org"})
	if code != 200 || steps(out)["SES transport"] != "ok" || r.mc.domains["two.org"]["relayhost"] != rh["id"] || len(r.mc.relayhosts) != 1 {
		t.Fatalf("second domain: %d %v %v", code, out, r.mc.relayhosts)
	}

	// Overview shows both, with the transport flag.
	code, out = r.do("GET", "/overview", nil)
	if code != 200 || len(out["domains"].([]any)) != 2 || !out["domains"].([]any)[0].(map[string]any)["ses_transport"].(bool) {
		t.Fatalf("overview: %v", out)
	}

	// Turning the transport off and on.
	if code, out = r.do("PUT", "/domains/two.org", map[string]any{"ses_transport": false}); code != 200 || r.mc.domains["two.org"]["relayhost"] != "0" {
		t.Fatalf("off: %d %v", code, out)
	}
	if code, out = r.do("PUT", "/domains/two.org", map[string]any{"ses_transport": true}); code != 200 || r.mc.domains["two.org"]["relayhost"] != rh["id"] {
		t.Fatalf("on: %d %v", code, out)
	}

	// Duplicate domain.
	if code, _ = r.do("POST", "/domains", map[string]any{"domain": "two.org"}); code != 409 {
		t.Fatal("duplicate should be 409, got", code)
	}
}

func TestDomainWithoutSES(t *testing.T) {
	r := newRig(t)
	code, out := r.do("POST", "/domains", map[string]any{"domain": "plain.net", "ses": false})
	if code != 200 || steps(out)["Amazon SES"] != "skip" || len(r.ses.ids) != 0 {
		t.Fatal(code, out)
	}
	for _, rec := range out["records"].([]any) {
		rec := rec.(map[string]any)
		if strings.Contains(rec["content"].(string), "amazonses") {
			t.Fatalf("SES record for a non-SES domain: %v", rec)
		}
	}
}

func TestMailboxesAliasesAndDelete(t *testing.T) {
	r := newRig(t)
	if code, out := r.do("POST", "/domains", map[string]any{"domain": "shop.io"}); code != 200 {
		t.Fatal(out)
	}
	code, out := r.do("POST", "/domains/shop.io/mailboxes", map[string]any{"local_part": "Hello", "name": "Hello"})
	if code != 200 || out["address"] != "hello@shop.io" || len(out["password"].(string)) != 20 {
		t.Fatal(code, out)
	}
	if code, _ = r.do("POST", "/domains/shop.io/mailboxes", map[string]any{"local_part": "bad local"}); code != 400 {
		t.Fatal("bad local part accepted")
	}
	if code, out = r.do("POST", "/domains/shop.io/aliases", map[string]any{"local_part": "sales", "goto": "hello@shop.io, me@else.com"}); code != 200 {
		t.Fatal(out)
	}
	if r.mc.aliases[0]["goto"] != "hello@shop.io,me@else.com" {
		t.Fatal(r.mc.aliases)
	}
	code, out = r.do("GET", "/domains/shop.io", nil)
	if code != 200 || len(out["mailboxes"].([]any)) != 1 || len(out["aliases"].([]any)) != 1 || out["ses"].(map[string]any)["identity"] != true {
		t.Fatal(code, out)
	}
	if code, _ = r.do("DELETE", "/domains/shop.io/aliases/99", nil); code != 404 {
		t.Fatal("unknown alias should 404")
	}
	if code, _ = r.do("DELETE", "/domains/shop.io?ses=1", nil); code != 409 {
		t.Fatal("domain with mailboxes must not be deleted")
	}
	r.mc.mailboxes = nil
	if code, out = r.do("DELETE", "/domains/shop.io?ses=1", nil); code != 200 {
		t.Fatal(out)
	}
	if _, ok := r.mc.domains["shop.io"]; ok {
		t.Fatal("domain not deleted")
	}
	if _, ok := r.ses.ids["shop.io"]; ok {
		t.Fatal("SES identity not deleted")
	}
}

func TestDisabledPluginRefuses(t *testing.T) {
	r := newRig(t)
	if _, err := r.reg.Save(context.Background(), "mailcow", false, plugin.Config{}); err != nil {
		t.Fatal(err)
	}
	if code, _ := r.do("GET", "/overview", nil); code != 409 {
		t.Fatal("disabled plugin answered", code)
	}
}

func TestPluginTest(t *testing.T) {
	r := newRig(t)
	r.p.dns = nil
	msg, err := r.reg.Test(context.Background(), "mailcow", plugin.Config{"cf_token": "-"})
	if err != nil || !strings.Contains(msg, "mailcow 2025-10") || !strings.Contains(msg, "sandbox") {
		t.Fatal(msg, err)
	}
	if _, err := r.reg.Test(context.Background(), "mailcow", plugin.Config{"api_key": "wrong", "cf_token": "-"}); err == nil {
		t.Fatal("wrong key should fail")
	}
}

func TestQuoteTXT(t *testing.T) {
	long := strings.Repeat("x", 600)
	q := quoteTXT(long)
	if normTXT(q) != long || strings.Count(q, `"`) != 6 {
		t.Fatal(q)
	}
	if !same(Record{Type: "TXT", Content: long}, q) {
		t.Fatal("split TXT should match")
	}
	if !same(Record{Type: "MX", Content: "mail.x.com"}, "10 Mail.X.com.") {
		t.Fatal("MX compare")
	}
}
