// Package cloudflare is the Cloudflare plugin: cache purges for sites and
// Cloudflare Access sign-in for the panel. It is for the server's admins only.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// API is Cloudflare's API base (tests point it elsewhere).
var API = "https://api.cloudflare.com/client/v4"

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// Client calls the Cloudflare API with a token.
type Client struct {
	Token string
	HTTP  *http.Client
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, API+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare: %w", err)
	}
	defer res.Body.Close()
	var env cfEnvelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return fmt.Errorf("Cloudflare answered %s", res.Status)
	}
	if !env.Success {
		var msgs []string
		for _, m := range env.Errors {
			msgs = append(msgs, m.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, res.Status)
		}
		return errors.New("Cloudflare: " + strings.Join(msgs, "; "))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// zoneFor finds the Cloudflare zone that holds a hostname.
func (c *Client) zoneFor(ctx context.Context, host string) (string, string, error) {
	labels := strings.Split(host, ".")
	for i := 0; i < len(labels)-1; i++ {
		name := strings.Join(labels[i:], ".")
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := c.call(ctx, "GET", "/zones?name="+url.QueryEscape(name), nil, &zones); err != nil {
			return "", "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, zones[0].Name, nil
		}
	}
	return "", "", fmt.Errorf("no Cloudflare zone for %s is visible to the token", host)
}

// Purge clears Cloudflare's cache for hostnames, zone by zone.
func (c *Client) Purge(ctx context.Context, hostnames []string) (int, error) {
	byZone := map[string][]string{}
	for _, h := range hostnames {
		id, _, err := c.zoneFor(ctx, h)
		if err != nil {
			return 0, err
		}
		byZone[id] = append(byZone[id], h)
	}
	n := 0
	for id, hosts := range byZone {
		if err := c.call(ctx, "POST", "/zones/"+id+"/purge_cache", map[string]any{"hosts": hosts}, nil); err != nil {
			return n, err
		}
		n += len(hosts)
	}
	return n, nil
}

// Verify checks that the token works and lists the zones it sees.
func (c *Client) Verify(ctx context.Context) (string, error) {
	var res struct {
		Status string `json:"status"`
	}
	if err := c.call(ctx, "GET", "/user/tokens/verify", nil, &res); err != nil {
		return "", err
	}
	var zones []struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "GET", "/zones?per_page=50", nil, &zones); err != nil {
		return "", err
	}
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		names = append(names, z.Name)
	}
	if len(names) == 0 {
		return "", errors.New("the token works but sees no zones; give it Zone → Cache Purge on your zones")
	}
	return "Token " + res.Status + ". Zones: " + strings.Join(names, ", "), nil
}
