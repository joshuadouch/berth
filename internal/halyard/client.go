// Package halyard talks to a Halyard load balancer's REST API.
package halyard

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls the Halyard API with an API token.
type Client struct {
	URL      string
	Token    string
	Insecure bool // accept Halyard's self-signed UI certificate
	HTTP     *http.Client
}

// ErrNotFound is a 404 from Halyard.
var ErrNotFound = errors.New("not found on Halyard")

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: c.Insecure} //nolint:gosec // opt-in for a self-signed LAN UI
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// Do sends a request and decodes the JSON response into out.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if c.URL == "" || c.Token == "" {
		return errors.New("Halyard is not connected; add its URL and an API token in Settings")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "berth")
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("Halyard: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 404 {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error  string `json:"error"`
			Output string `json:"output"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if resp.StatusCode == 401 {
			msg += " (check the API token)"
		}
		if e.Output != "" {
			msg += "\n" + e.Output
		}
		return fmt.Errorf("Halyard HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// VIP is a Halyard VIP.
type VIP struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Label   string `json:"label"`
}

// Pool is a Halyard pool (only the fields Berth reads).
type Pool struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Members []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"members"`
}

// Cert is a Halyard certificate.
type Cert struct {
	ID      string   `json:"id"`
	Domains []string `json:"domains"`
	Status  string   `json:"status"`
	Hosts   int      `json:"hosts"`
}

// Cloudflare is what Berth needs to know about Halyard's Cloudflare setup.
type Cloudflare struct {
	TokenSet bool   `json:"token_set"`
	PublicIP string `json:"public_ip"`
	TunnelID string `json:"tunnel_id"`
}

// Cloudflare reads Halyard's Cloudflare settings without calling Cloudflare.
func (c *Client) Cloudflare(ctx context.Context) (Cloudflare, error) {
	var v Cloudflare
	return v, c.Do(ctx, "GET", "/api/cloudflare?light=1", nil, &v)
}

// VIPs lists VIPs.
func (c *Client) VIPs(ctx context.Context) ([]VIP, error) {
	var v []VIP
	return v, c.Do(ctx, "GET", "/api/vips", nil, &v)
}

// Pools lists pools.
func (c *Client) Pools(ctx context.Context) ([]Pool, error) {
	var v []Pool
	return v, c.Do(ctx, "GET", "/api/pools", nil, &v)
}

// Certs lists certificates.
func (c *Client) Certs(ctx context.Context) ([]Cert, error) {
	var v []Cert
	return v, c.Do(ctx, "GET", "/api/certs", nil, &v)
}

// Host is a Halyard host kept as a map, so fields Berth does not know about
// survive an update.
type Host map[string]any

// Hosts lists every host.
func (c *Client) Hosts(ctx context.Context) ([]Host, error) {
	var v []Host
	return v, c.Do(ctx, "GET", "/api/hosts", nil, &v)
}

// GetHost loads a host.
func (c *Client) GetHost(ctx context.Context, id string) (Host, error) {
	var h Host
	return h, c.Do(ctx, "GET", "/api/hosts/"+url.PathEscape(id), nil, &h)
}

// SaveHost creates (empty id) or replaces a host and returns it.
func (c *Client) SaveHost(ctx context.Context, id string, h Host) (Host, error) {
	delete(h, "id")
	var out Host
	if id == "" {
		return out, c.Do(ctx, "POST", "/api/hosts", h, &out)
	}
	return out, c.Do(ctx, "PUT", "/api/hosts/"+url.PathEscape(id), h, &out)
}

// DeleteHost removes a host; a missing host is not an error.
func (c *Client) DeleteHost(ctx context.Context, id string) error {
	err := c.Do(ctx, "DELETE", "/api/hosts/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// CreateCert asks Halyard to issue a certificate for domains.
func (c *Client) CreateCert(ctx context.Context, domains []string) (Cert, error) {
	var out Cert
	return out, c.Do(ctx, "POST", "/api/certs", map[string]any{"domains": domains}, &out)
}

// DeleteCert removes a certificate.
func (c *Client) DeleteCert(ctx context.Context, id string) error {
	err := c.Do(ctx, "DELETE", "/api/certs/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// HealthHost is sent as the Host header of the pool's health check. HAProxy
// checks speak HTTP/1.1, and nginx answers 400 to HTTP/1.1 without a Host;
// a name no site uses lands on Berth's default server, which serves
// /berth-health.
const HealthHost = "berth-health"

// FixHealthHost adds the Host header to a Berth pool's health check made
// before Berth set one. It reports whether it changed the pool.
func (c *Client) FixHealthHost(ctx context.Context, id string) (bool, error) {
	var p map[string]any
	if err := c.Do(ctx, "GET", "/api/pools/"+url.PathEscape(id), nil, &p); err != nil {
		return false, err
	}
	h, _ := p["health"].(map[string]any)
	if h == nil || h["path"] != "/berth-health" || h["type"] != "http" {
		return false, nil
	}
	if hh, _ := h["host_header"].(string); hh != "" {
		return false, nil
	}
	h["host_header"] = HealthHost
	delete(p, "id")
	return true, c.Do(ctx, "PUT", "/api/pools/"+url.PathEscape(id), p, nil)
}

// CreatePool adds a pool that sends traffic to Berth's nginx on addr (ip:80).
func (c *Client) CreatePool(ctx context.Context, name, member, addr string) (Pool, error) {
	var out Pool
	body := map[string]any{
		"name": name, "mode": "http", "algorithm": "roundrobin",
		"health": map[string]any{"enabled": true, "type": "http", "method": "GET", "path": "/berth-health",
			"host_header": HealthHost, "expect_status": "200", "interval_sec": 5, "fall": 3, "rise": 2},
		"members": []map[string]any{{"name": member, "address": addr, "weight": 100, "state": "ready"}},
	}
	return out, c.Do(ctx, "POST", "/api/pools", body, &out)
}

// Covers reports whether a certificate's names cover every hostname.
func Covers(certDomains, hostnames []string) bool {
	for _, h := range hostnames {
		ok := false
		for _, d := range certDomains {
			d = strings.ToLower(d)
			if d == h {
				ok = true
				break
			}
			if strings.HasPrefix(d, "*.") {
				if i := strings.Index(h, "."); i > 0 && h[i+1:] == d[2:] {
					ok = true
					break
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// LocalAddr returns the address this machine uses to reach the Halyard URL,
// which is where Halyard should send site traffic.
func LocalAddr(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, port), 3*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
