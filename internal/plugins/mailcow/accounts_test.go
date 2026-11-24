package mailcow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/plugin"
)

// customerRig adds a customer on a mail plan and makes requests as them.
func customerRig(t *testing.T, pl model.Plan) (*rig, model.Account) {
	r := newRig(t)
	ctx := context.Background()
	pl.ID, pl.Name = "plan-mail", "Business Email"
	if err := r.st.SavePlan(ctx, pl); err != nil {
		t.Fatal(err)
	}
	a := model.Account{ID: "acc-cust", Name: "Dana", Email: "dana@example.net", PlanID: pl.ID}
	if err := r.st.SaveAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	u := model.User{ID: "u-cust", Username: "dana", Role: model.RoleCustomer, AccountID: a.ID, PasswordHash: "x"}
	if err := r.st.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	r.user = u
	return r, a
}

func TestExistingDomainsBelongToAdmin(t *testing.T) {
	r := newRig(t)
	r.mc.domains["old.example"] = map[string]any{"domain_name": "old.example", "active": "1", "relayhost": "0"}
	code, out := r.do("GET", "/overview", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	ds := out["domains"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["owner"] != "acc-admin" {
		t.Fatalf("existing domain should be the admin's: %v", ds)
	}
	if r.p.AllowAccount(context.Background(), "acc-admin") {
		t.Fatal("the gate is for plans and explicit owners only")
	}
}

func TestCustomerMailDomain(t *testing.T) {
	r, a := customerRig(t, model.Plan{MailDomains: 1, Mailboxes: 2, MailAliases: 1, MailStorageMB: 4096})
	ctx := context.Background()
	if !r.reg.ActiveFor(ctx, "mailcow", a.ID) {
		t.Fatal("a mail plan should open the Mail page")
	}
	if r.reg.ActiveFor(ctx, "mailcow", "acc-nobody") {
		t.Fatal("accounts without mail must not get the page")
	}
	// The admin's domain stays hidden.
	r.mc.domains["admin.example"] = map[string]any{"domain_name": "admin.example", "active": "1", "relayhost": "0"}

	// Unproven domain: told which TXT record to add.
	code, out := r.do("POST", "/domains", map[string]any{"domain": "dana.shop", "quota_mb": 99999, "ses": false})
	if code != 403 || out["verify"] == nil {
		t.Fatal(code, out)
	}
	v := out["verify"].(map[string]any)
	r.dns.recs["TXT "+v["name"].(string)] = []string{`"` + v["value"].(string) + `"`}
	if code, out = r.do("POST", "/domains", map[string]any{"domain": "dana.shop", "quota_mb": 99999, "ses": false}); code != 200 {
		t.Fatal(code, out)
	}
	d := r.mc.domains["dana.shop"]
	if fmt.Sprint(d["max_quota_for_domain"]) != fmt.Sprint(4096<<20) || fmt.Sprint(d["max_num_mboxes_for_domain"]) != "2" {
		t.Fatalf("plan limits not applied: %v", d)
	}
	if _, ok := r.ses.ids["dana.shop"]; !ok {
		t.Fatal("customers cannot opt out of SES")
	}
	// Second domain is over the plan.
	if code, _ = r.do("POST", "/domains", map[string]any{"domain": "other.shop"}); code != 409 {
		t.Fatal("domain limit", code)
	}
	code, out = r.do("GET", "/overview", nil)
	if ds := out["domains"].([]any); code != 200 || len(ds) != 1 || out["customer"] != true || out["url"] != nil {
		t.Fatalf("customer overview: %d %v", code, out)
	}
	if code, _ = r.do("GET", "/domains/admin.example", nil); code != 404 {
		t.Fatal("other domains must be hidden", code)
	}
	if code, _ = r.do("PUT", "/domains/dana.shop", map[string]any{"ses_transport": false}); code != 403 {
		t.Fatal("customers cannot change SES", code)
	}
	// Mailboxes up to the plan, then refused.
	for _, l := range []string{"a", "b"} {
		if code, out = r.do("POST", "/domains/dana.shop/mailboxes", map[string]any{"local_part": l}); code != 200 {
			t.Fatal(code, out)
		}
	}
	if code, _ = r.do("POST", "/domains/dana.shop/mailboxes", map[string]any{"local_part": "c"}); code != 409 {
		t.Fatal("mailbox limit", code)
	}
	if code, _ = r.do("POST", "/domains/admin.example/mailboxes", map[string]any{"local_part": "x"}); code != 404 {
		t.Fatal("mailbox on someone else's domain", code)
	}
	if code, _ = r.do("PUT", "/mailboxes/x@admin.example", map[string]any{"password": ""}); code != 404 {
		t.Fatal("password of someone else's mailbox", code)
	}
	if code, _ = r.do("POST", "/domains/dana.shop/aliases", map[string]any{"local_part": "hi", "goto": "a@dana.shop"}); code != 200 {
		t.Fatal(code)
	}
	if code, _ = r.do("POST", "/domains/dana.shop/aliases", map[string]any{"local_part": "yo", "goto": "a@dana.shop"}); code != 409 {
		t.Fatal("alias limit", code)
	}
	if code, _ = r.do("PUT", "/domains/dana.shop/owner", map[string]any{"account": a.ID}); code != 403 {
		t.Fatal("customers cannot move domains", code)
	}
}

func TestProvisionSuspendDelete(t *testing.T) {
	r, a := customerRig(t, model.Plan{MailDomains: 1, Mailboxes: 5, MailStorageMB: 10240})
	ctx := context.Background()
	body, _ := json.Marshal(map[string]string{"domain": "Dana.Shop"})
	out, err := r.reg.Provision(ctx, "mailcow", a, body)
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["created"] != true || !strings.HasPrefix(m["webmail"].(string), "https://") {
		t.Fatal(out)
	}
	if r.p.ownerOf(ctx, "dana.shop") != a.ID {
		t.Fatal("not owned")
	}
	// Again: no error, nothing new.
	if out, err = r.reg.Provision(ctx, "mailcow", a, body); err != nil || out.(map[string]any)["created"] != false {
		t.Fatal(out, err)
	}
	other := model.Account{ID: "acc-2", Name: "Eve"}
	if _, err = r.reg.Provision(ctx, "mailcow", other, body); err == nil {
		t.Fatal("another account took the domain")
	}
	r.user = model.User{ID: "u-cust", Role: model.RoleCustomer, AccountID: a.ID}
	if code, out := r.do("POST", "/domains/dana.shop/mailboxes", map[string]any{"local_part": "info"}); code != 200 {
		t.Fatal(code, out)
	}

	r.reg.AccountChanged(ctx, a, plugin.AccountSuspended)
	if r.mc.domains["dana.shop"]["active"] != "0" {
		t.Fatal("suspending should pause the domain")
	}
	r.reg.AccountChanged(ctx, a, plugin.AccountUnsuspended)
	if r.mc.domains["dana.shop"]["active"] != "1" {
		t.Fatal("unsuspending should resume the domain")
	}
	r.reg.AccountChanged(ctx, a, plugin.AccountDeleted)
	if _, ok := r.mc.domains["dana.shop"]; ok || len(r.mc.mailboxes) != 0 {
		t.Fatal("deleting the account should remove its mail")
	}
	if _, ok := r.ses.ids["dana.shop"]; ok {
		t.Fatal("SES identity left behind")
	}
	if len(r.p.owned(ctx, a.ID)) != 0 {
		t.Fatal("owner entry left behind")
	}
}

func TestAdminMovesDomain(t *testing.T) {
	r, a := customerRig(t, model.Plan{})
	r.user = model.User{ID: "u-admin", Role: model.RoleAdmin, AccountID: "acc-admin"}
	r.mc.domains["gift.example"] = map[string]any{"domain_name": "gift.example", "active": "1", "relayhost": "0"}
	if code, out := r.do("PUT", "/domains/gift.example/owner", map[string]any{"account": a.ID}); code != 200 {
		t.Fatal(code, out)
	}
	// No mail on the plan, but owning a domain opens the page.
	if !r.reg.ActiveFor(context.Background(), "mailcow", a.ID) {
		t.Fatal("owning a domain should open the Mail page")
	}
}

type fakeZones struct{ added []string }

func (f *fakeZones) ZoneOf(_ context.Context, account, host string) string {
	if account == "acc-cust" && strings.HasSuffix(host, "dana.shop") {
		return "dana.shop"
	}
	return ""
}
func (f *fakeZones) Records(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}
func (f *fakeZones) AddRecord(_ context.Context, zone, typ, name, content string) error {
	f.added = append(f.added, typ+" "+name+" "+content)
	return nil
}

func TestBerthZoneRecords(t *testing.T) {
	r := newRig(t)
	z := &fakeZones{}
	r.p.dns, r.p.zoneHost = nil, z
	ctx := context.Background()
	c, _ := r.p.host.Config(ctx)
	d, mode := r.p.dnsForDomain(ctx, c, "acc-cust", "dana.shop")
	if mode != "berth" {
		t.Fatal(mode)
	}
	recs := Check(ctx, d, Plan{Domain: "dana.shop", MailHost: "mail.host.net"}.Records(), true)
	if len(recs) == 0 || recs[0].Status != "created" {
		t.Fatal(recs)
	}
	if z.added[0] != "MX dana.shop 10 mail.host.net." || !strings.HasPrefix(z.added[1], `TXT dana.shop "v=spf1`) {
		t.Fatal(z.added)
	}
	if !r.p.proven(ctx, "acc-cust", "dana.shop") {
		t.Fatal("a customer's own zone proves the domain")
	}
}
