package engine

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

	"github.com/joshuadouch/berth/internal/model"
)

// cloudflareAPI is Cloudflare's API base (tests point it elsewhere).
var cloudflareAPI = "https://api.cloudflare.com/client/v4"

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (e *Engine) cloudflare(ctx context.Context, token, method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, cloudflareAPI+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := e.httpClient(30 * time.Second).Do(req)
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
func (e *Engine) zoneFor(ctx context.Context, token, host string) (string, string, error) {
	labels := strings.Split(host, ".")
	for i := 0; i < len(labels)-1; i++ {
		name := strings.Join(labels[i:], ".")
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := e.cloudflare(ctx, token, "GET", "/zones?name="+url.QueryEscape(name), nil, &zones); err != nil {
			return "", "", err
		}
		if len(zones) > 0 {
			return zones[0].ID, zones[0].Name, nil
		}
	}
	return "", "", fmt.Errorf("no Cloudflare zone for %s is visible to the token", host)
}

// PurgeCloudflare clears Cloudflare's cache for every hostname of a site.
func (e *Engine) PurgeCloudflare(ctx context.Context, s model.Site) (int, error) {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return 0, err
	}
	if st.CloudflareToken == "" {
		return 0, errors.New("add a Cloudflare token under Settings → Cloudflare first")
	}
	byZone := map[string][]string{}
	for _, h := range s.Hostnames() {
		id, _, err := e.zoneFor(ctx, st.CloudflareToken, h)
		if err != nil {
			return 0, err
		}
		byZone[id] = append(byZone[id], h)
	}
	n := 0
	for id, hosts := range byZone {
		if err := e.cloudflare(ctx, st.CloudflareToken, "POST", "/zones/"+id+"/purge_cache", map[string]any{"hosts": hosts}, nil); err != nil {
			return n, err
		}
		n += len(hosts)
	}
	return n, nil
}

// TestCloudflare checks that the token works.
func (e *Engine) TestCloudflare(ctx context.Context, token string) (string, error) {
	var res struct {
		Status string `json:"status"`
	}
	if err := e.cloudflare(ctx, token, "GET", "/user/tokens/verify", nil, &res); err != nil {
		return "", err
	}
	var zones []struct {
		Name string `json:"name"`
	}
	if err := e.cloudflare(ctx, token, "GET", "/zones?per_page=50", nil, &zones); err != nil {
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
