package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPurge(t *testing.T) {
	var purged []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
			return
		}
		switch {
		case r.URL.Path == "/zones" && r.URL.Query().Get("name") == "example.co.uk":
			io.WriteString(w, `{"success":true,"result":[{"id":"z1","name":"example.co.uk"}]}`)
		case r.URL.Path == "/zones":
			io.WriteString(w, `{"success":true,"result":[]}`)
		case r.URL.Path == "/zones/z1/purge_cache":
			var body struct{ Hosts []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			purged = append(purged, body.Hosts...)
			io.WriteString(w, `{"success":true,"result":{"id":"z1"}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := API
	API = srv.URL
	defer func() { API = old }()
	ctx := context.Background()
	if _, err := (&Client{Token: "bad"}).Purge(ctx, []string{"example.co.uk"}); err == nil || !strings.Contains(err.Error(), "Authentication") {
		t.Fatalf("bad token: %v", err)
	}
	n, err := (&Client{Token: "tok"}).Purge(ctx, []string{"example.co.uk", "www.example.co.uk"})
	if err != nil || n != 2 || strings.Join(purged, ",") != "example.co.uk,www.example.co.uk" {
		t.Fatalf("purge %d %v %v", n, err, purged)
	}
}
