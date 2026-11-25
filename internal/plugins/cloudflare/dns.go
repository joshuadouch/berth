package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Comments Cloudflare records carry so each program knows its own.
const (
	ManagedComment = "managed by berth"
	HalyardComment = "managed by halyard"
	acmeComment    = "berth acme challenge"
)

// Record is a Cloudflare DNS record.
type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
	Comment string `json:"comment"`
}

// Zone finds the Cloudflare zone that holds a hostname.
func (c *Client) Zone(ctx context.Context, host string) (id, name string, err error) {
	return c.zoneFor(ctx, host)
}

// Records lists the records with a name in a zone.
func (c *Client) Records(ctx context.Context, zone, name string) ([]Record, error) {
	var out []Record
	err := c.call(ctx, "GET", "/zones/"+zone+"/dns_records?per_page=100&name="+url.QueryEscape(name), nil, &out)
	return out, err
}

// CreateRecord adds a record.
func (c *Client) CreateRecord(ctx context.Context, zone string, r Record) (Record, error) {
	var out Record
	r.ID = ""
	err := c.call(ctx, "POST", "/zones/"+zone+"/dns_records", r, &out)
	return out, err
}

// UpdateRecord replaces a record in place.
func (c *Client) UpdateRecord(ctx context.Context, zone string, r Record) error {
	id := r.ID
	r.ID = ""
	return c.call(ctx, "PUT", "/zones/"+zone+"/dns_records/"+id, r, nil)
}

// DeleteRecord removes a record.
func (c *Client) DeleteRecord(ctx context.Context, zone, id string) error {
	return c.call(ctx, "DELETE", "/zones/"+zone+"/dns_records/"+id, nil, nil)
}

// RecordState is how one of a site's names looks in Cloudflare.
type RecordState struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // ok, conflict, no_zone, error
	Detail  string `json:"detail,omitempty"`
	Content string `json:"content,omitempty"`
	Proxied bool   `json:"proxied"`
}

// Record states.
const (
	StateOK       = "ok"
	StateConflict = "conflict"
	StateNoZone   = "no_zone"
	StateError    = "error"
)

// Want is what a site's names should point at.
type Want struct {
	IPv4, IPv6 string
	Proxied    bool
	// Adopt replaces records Berth did not create. Records Halyard created
	// are always taken over, since the site has left Halyard.
	Adopt bool
}

func addrRecord(r Record) bool { return r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME" }

// Ensure makes name's A and AAAA records point at this server, taking over
// Halyard's records in place (so visitors never see a gap) and leaving
// records someone else made alone unless w.Adopt.
func (c *Client) Ensure(ctx context.Context, name string, w Want) RecordState {
	st := RecordState{Name: name, Proxied: w.Proxied}
	zone, _, err := c.zoneFor(ctx, name)
	if err != nil {
		if strings.Contains(err.Error(), "no Cloudflare zone") {
			st.Status, st.Detail = StateNoZone, "No Cloudflare zone for this name is visible to the token"
		} else {
			st.Status, st.Detail = StateError, err.Error()
		}
		return st
	}
	recs, err := c.Records(ctx, zone, name)
	if err != nil {
		st.Status, st.Detail = StateError, err.Error()
		return st
	}
	want := map[string]string{}
	if w.IPv4 != "" {
		want["A"] = w.IPv4
	}
	if w.IPv6 != "" {
		want["AAAA"] = w.IPv6
	}
	var mine []Record
	for _, r := range recs {
		if !addrRecord(r) || !strings.EqualFold(r.Name, name) {
			continue
		}
		switch {
		case r.Comment == ManagedComment, r.Comment == HalyardComment, w.Adopt:
			mine = append(mine, r)
		default:
			st.Status = StateConflict
			st.Detail = fmt.Sprintf("A %s record Berth did not create already exists (%s); left as it is", r.Type, r.Content)
			st.Content = r.Content
			return st
		}
	}
	// Update records of the right type in place; delete the rest first so a
	// CNAME never blocks the new A record.
	used := map[string]bool{}
	var update, drop []Record
	for _, r := range mine {
		if v, ok := want[r.Type]; ok && !used[r.Type] {
			used[r.Type] = true
			if r.Content != v || r.Proxied != w.Proxied || r.Comment != ManagedComment {
				r.Content, r.Proxied, r.Comment, r.TTL = v, w.Proxied, ManagedComment, 1
				update = append(update, r)
			}
			continue
		}
		drop = append(drop, r)
	}
	for _, r := range update {
		if err := c.UpdateRecord(ctx, zone, r); err != nil {
			st.Status, st.Detail = StateError, err.Error()
			return st
		}
	}
	for _, r := range drop {
		if err := c.DeleteRecord(ctx, zone, r.ID); err != nil {
			st.Status, st.Detail = StateError, err.Error()
			return st
		}
	}
	types := make([]string, 0, len(want))
	for t := range want {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		if used[t] {
			continue
		}
		if _, err := c.CreateRecord(ctx, zone, Record{Type: t, Name: name, Content: want[t], Proxied: w.Proxied, TTL: 1, Comment: ManagedComment}); err != nil {
			st.Status, st.Detail = StateError, err.Error()
			return st
		}
	}
	st.Status = StateOK
	st.Content = strings.TrimSpace(w.IPv4 + " " + w.IPv6)
	return st
}

// Release deletes the records Berth made for name.
func (c *Client) Release(ctx context.Context, name string) error {
	zone, _, err := c.zoneFor(ctx, name)
	if err != nil {
		return nil // nothing of ours can be there
	}
	recs, err := c.Records(ctx, zone, name)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Comment == ManagedComment && addrRecord(r) && strings.EqualFold(r.Name, name) {
			if err := c.DeleteRecord(ctx, zone, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// LookupTXT is how PresentTXT checks a record is visible (tests replace it).
var LookupTXT = func(ctx context.Context, name string) ([]string, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, "1.1.1.1:53")
	}}
	return r.LookupTXT(ctx, name)
}

// propagation is how long PresentTXT waits for a record to show.
var propagation = 2 * time.Minute

// PresentTXT adds a TXT record for a Let's Encrypt DNS-01 check and waits
// until Cloudflare's resolver returns it.
func (c *Client) PresentTXT(ctx context.Context, fqdn, value string) (func(), error) {
	zone, _, err := c.zoneFor(ctx, strings.TrimPrefix(fqdn, "_acme-challenge."))
	if err != nil {
		return nil, err
	}
	rec, err := c.CreateRecord(ctx, zone, Record{Type: "TXT", Name: fqdn, Content: value, TTL: 60, Comment: acmeComment})
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.DeleteRecord(ctx, zone, rec.ID)
	}
	deadline := time.Now().Add(propagation)
	for {
		txts, _ := LookupTXT(ctx, fqdn)
		for _, t := range txts {
			if strings.Trim(t, "\"") == value {
				return cleanup, nil
			}
		}
		if time.Now().After(deadline) {
			cleanup()
			return nil, errors.New("the TXT record for " + fqdn + " did not show up in DNS in time")
		}
		select {
		case <-ctx.Done():
			cleanup()
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
