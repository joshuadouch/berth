package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCF is a Cloudflare API with one zone, example.com, held in memory.
type fakeCF struct {
	mu   sync.Mutex
	recs map[string]Record
	next int
	ops  []string
}

func newFakeCF(t *testing.T, recs ...Record) *fakeCF {
	f := &fakeCF{recs: map[string]Record{}}
	for _, r := range recs {
		f.next++
		r.ID = fmt.Sprintf("r%d", f.next)
		f.recs[r.ID] = r
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
	return f
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, `{"success":true,"result":%s}`, b)
	}
	path := r.URL.Path
	switch {
	case path == "/zones":
		if r.URL.Query().Get("name") == "example.com" {
			ok([]map[string]string{{"id": "z1", "name": "example.com"}})
		} else {
			ok([]any{})
		}
	case path == "/zones/z1/dns_records" && r.Method == "GET":
		out := []Record{}
		for _, rec := range f.recs {
			if rec.Name == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		ok(out)
	case path == "/zones/z1/dns_records" && r.Method == "POST":
		var rec Record
		_ = json.NewDecoder(r.Body).Decode(&rec)
		for _, e := range f.recs {
			if e.Name == rec.Name && (e.Type == "CNAME") != (rec.Type == "CNAME") && rec.Type != "TXT" && e.Type != "TXT" {
				w.WriteHeader(400)
				io.WriteString(w, `{"success":false,"errors":[{"code":81053,"message":"An A, AAAA, or CNAME record with that host already exists."}]}`)
				return
			}
		}
		f.next++
		rec.ID = fmt.Sprintf("r%d", f.next)
		f.recs[rec.ID] = rec
		f.ops = append(f.ops, "create "+rec.Type+" "+rec.Name)
		ok(rec)
	case strings.HasPrefix(path, "/zones/z1/dns_records/"):
		id := strings.TrimPrefix(path, "/zones/z1/dns_records/")
		if _, found := f.recs[id]; !found {
			w.WriteHeader(404)
			io.WriteString(w, `{"success":false,"errors":[{"code":81044,"message":"Record does not exist."}]}`)
			return
		}
		switch r.Method {
		case "PUT":
			var rec Record
			_ = json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = id
			f.recs[id] = rec
			f.ops = append(f.ops, "update "+rec.Type+" "+rec.Name)
		case "DELETE":
			f.ops = append(f.ops, "delete "+f.recs[id].Type+" "+f.recs[id].Name)
			delete(f.recs, id)
		}
		ok(map[string]string{"id": id})
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"success":false,"errors":[{"code":7003,"message":"no route"}]}`)
	}
}

func (f *fakeCF) find(name string) []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, r := range f.recs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func TestEnsureTakesOverHalyardInPlace(t *testing.T) {
	f := newFakeCF(t,
		Record{Type: "A", Name: "example.com", Content: "198.51.100.1", Proxied: true, Comment: HalyardComment},
		Record{Type: "CNAME", Name: "www.example.com", Content: "abc.cfargotunnel.com", Proxied: true, Comment: HalyardComment},
		Record{Type: "A", Name: "shop.example.com", Content: "192.0.2.9", Comment: "made by hand"},
		Record{Type: "MX", Name: "example.com", Content: "mail.example.com"},
	)
	c := &Client{Token: "t"}
	ctx := context.Background()
	w := Want{IPv4: "203.0.113.10", Proxied: true}

	// The apex A record is rewritten in place: no delete, so no gap.
	if st := c.Ensure(ctx, "example.com", w); st.Status != StateOK {
		t.Fatalf("apex: %+v", st)
	}
	if strings.Join(f.ops, ",") != "update A example.com" {
		t.Errorf("apex ops %v", f.ops)
	}
	recs := f.find("example.com")
	if len(recs) != 2 {
		t.Fatalf("MX record touched: %+v", recs)
	}
	for _, r := range recs {
		if r.Type == "A" && (r.Content != "203.0.113.10" || r.Comment != ManagedComment || !r.Proxied) {
			t.Errorf("apex record %+v", r)
		}
	}

	// A tunnel CNAME is replaced with an A record.
	f.ops = nil
	if st := c.Ensure(ctx, "www.example.com", w); st.Status != StateOK {
		t.Fatalf("www: %+v", st)
	}
	if strings.Join(f.ops, ",") != "delete CNAME www.example.com,create A www.example.com" {
		t.Errorf("www ops %v", f.ops)
	}

	// Someone else's record is left alone until adopted.
	f.ops = nil
	if st := c.Ensure(ctx, "shop.example.com", w); st.Status != StateConflict || len(f.ops) != 0 {
		t.Fatalf("shop: %+v %v", st, f.ops)
	}
	w.Adopt = true
	if st := c.Ensure(ctx, "shop.example.com", w); st.Status != StateOK || f.find("shop.example.com")[0].Content != "203.0.113.10" {
		t.Fatalf("shop adopted: %+v %+v", st, f.find("shop.example.com"))
	}

	// Names outside any zone are reported, not errors.
	if st := c.Ensure(ctx, "other.test", w); st.Status != StateNoZone {
		t.Errorf("other zone: %+v", st)
	}

	// Adding IPv6 adds an AAAA record; nothing changes when in sync.
	f.ops = nil
	w = Want{IPv4: "203.0.113.10", IPv6: "2001:db8::10", Proxied: true}
	c.Ensure(ctx, "example.com", w)
	c.Ensure(ctx, "example.com", w)
	if strings.Join(f.ops, ",") != "create AAAA example.com" {
		t.Errorf("ipv6 ops %v", f.ops)
	}

	// Release removes only Berth's records.
	if err := c.Release(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if recs := f.find("example.com"); len(recs) != 1 || recs[0].Type != "MX" {
		t.Errorf("after release: %+v", recs)
	}
}

func TestPresentTXT(t *testing.T) {
	f := newFakeCF(t)
	oldLookup, oldProp := LookupTXT, propagation
	t.Cleanup(func() { LookupTXT, propagation = oldLookup, oldProp })
	LookupTXT = func(ctx context.Context, name string) ([]string, error) {
		var out []string
		for _, r := range f.find(name) {
			out = append(out, r.Content)
		}
		return out, nil
	}
	propagation = time.Second
	c := &Client{Token: "t"}
	cleanup, err := c.PresentTXT(context.Background(), "_acme-challenge.www.example.com", "token-value")
	if err != nil {
		t.Fatal(err)
	}
	if recs := f.find("_acme-challenge.www.example.com"); len(recs) != 1 || recs[0].Type != "TXT" || recs[0].Content != "token-value" {
		t.Fatalf("txt %+v", recs)
	}
	cleanup()
	if recs := f.find("_acme-challenge.www.example.com"); len(recs) != 0 {
		t.Errorf("txt left behind: %+v", recs)
	}
	if _, err := c.PresentTXT(context.Background(), "_acme-challenge.nowhere.test", "x"); err == nil {
		t.Error("presented a record outside any zone")
	}
}
