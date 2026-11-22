package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PDNS talks to the PowerDNS Authoritative HTTP API.
type PDNS struct {
	URL  string // e.g. http://10.0.30.53:8081
	Key  string
	HTTP *http.Client
}

// RRSet is every record of one name and type.
type RRSet struct {
	Name       string   `json:"name"` // fully qualified, with the trailing dot
	Type       string   `json:"type"`
	TTL        int      `json:"ttl,omitempty"`
	Records    []Record `json:"records"`
	ChangeType string   `json:"changetype,omitempty"`
}

// Record is one record of an RRSet.
type Record struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

// Zone is a PowerDNS zone.
type Zone struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Serial      int64    `json:"serial,omitempty"`
	Masters     []string `json:"masters,omitempty"`
	RRSets      []RRSet  `json:"rrsets,omitempty"`
	Nameservers []string `json:"nameservers"`
	SOAEditAPI  string   `json:"soa_edit_api,omitempty"`
	APIRectify  bool     `json:"api_rectify,omitempty"`
}

// ErrNoZone is returned for a zone PowerDNS does not have.
var ErrNoZone = errors.New("no such zone")

// fqdn adds the trailing dot.
func fqdn(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func zoneURL(zone string) string {
	// PowerDNS zone IDs are the name with its trailing dot; "/" is "=2F".
	return "/zones/" + url.PathEscape(strings.ReplaceAll(fqdn(zone), "/", "=2F"))
}

func (c *PDNS) do(ctx context.Context, method, path string, in, out any) error {
	if c.URL == "" {
		return errors.New("the PowerDNS API address is not set")
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+"/api/v1/servers/localhost"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("PowerDNS at %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		switch {
		case resp.StatusCode == 404 || resp.StatusCode == 422 && strings.Contains(e.Error, "Could not find domain"):
			return ErrNoZone
		case resp.StatusCode == 401:
			return errors.New("PowerDNS refused the API key")
		case e.Error != "":
			return fmt.Errorf("PowerDNS: %s", e.Error)
		}
		return fmt.Errorf("PowerDNS answered %s", resp.Status)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Version returns the server's version (a cheap check of the address and key).
func (c *PDNS) Version(ctx context.Context) (string, error) {
	var s struct {
		Version string `json:"version"`
	}
	err := c.do(ctx, "GET", "", nil, &s)
	return s.Version, err
}

// Zones lists the server's zones (without records).
func (c *PDNS) Zones(ctx context.Context) ([]Zone, error) {
	var z []Zone
	err := c.do(ctx, "GET", "/zones", nil, &z)
	return z, err
}

// Zone loads a zone with its records.
func (c *PDNS) Zone(ctx context.Context, name string) (Zone, error) {
	var z Zone
	err := c.do(ctx, "GET", zoneURL(name), nil, &z)
	return z, err
}

// CreateZone creates a zone.
func (c *PDNS) CreateZone(ctx context.Context, z Zone) error {
	if z.Nameservers == nil {
		z.Nameservers = []string{}
	}
	return c.do(ctx, "POST", "/zones", z, nil)
}

// DeleteZone removes a zone; a zone that is not there is no error.
func (c *PDNS) DeleteZone(ctx context.Context, name string) error {
	err := c.do(ctx, "DELETE", zoneURL(name), nil, nil)
	if errors.Is(err, ErrNoZone) {
		return nil
	}
	return err
}

// Patch replaces or deletes RRSets (ChangeType REPLACE or DELETE).
func (c *PDNS) Patch(ctx context.Context, zone string, sets []RRSet) error {
	return c.do(ctx, "PATCH", zoneURL(zone), map[string]any{"rrsets": sets}, nil)
}

// Notify asks a primary to tell its secondaries about a change now.
func (c *PDNS) Notify(ctx context.Context, zone string) error {
	return c.do(ctx, "PUT", zoneURL(zone)+"/notify", nil, nil)
}

// Retrieve asks a secondary to transfer a zone from its primary now.
func (c *PDNS) Retrieve(ctx context.Context, zone string) error {
	return c.do(ctx, "PUT", zoneURL(zone)+"/axfr-retrieve", nil, nil)
}
