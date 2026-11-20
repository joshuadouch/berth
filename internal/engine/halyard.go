package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/joshuadouch/berth/internal/halyard"
	"github.com/joshuadouch/berth/internal/model"
)

// halyardClient returns a client for the saved Halyard settings.
func (e *Engine) halyardClient(ctx context.Context) (*halyard.Client, model.HalyardSettings, error) {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return nil, model.HalyardSettings{}, err
	}
	h := st.Halyard
	if h.URL == "" || h.Token == "" {
		return nil, h, errors.New("Halyard is not connected; add its URL and an API token in Settings")
	}
	c := &halyard.Client{URL: h.URL, Token: h.Token, Insecure: h.Insecure}
	if e.HalyardHTTP != nil {
		c.HTTP = e.HalyardHTTP
	}
	return c, h, nil
}

// HalyardInfo is shown on the Settings and site pages.
type HalyardInfo struct {
	Configured bool                `json:"configured"`
	Error      string              `json:"error,omitempty"`
	VIPs       []halyard.VIP       `json:"vips"`
	Pools      []halyard.Pool      `json:"pools"`
	PoolOK     bool                `json:"pool_ok"`
	LocalAddr  string              `json:"local_addr"`
	Cloudflare *halyard.Cloudflare `json:"cloudflare,omitempty"`
}

// HalyardInfo reports the integration's state and what Halyard offers.
func (e *Engine) HalyardInfo(ctx context.Context) HalyardInfo {
	info := HalyardInfo{VIPs: []halyard.VIP{}, Pools: []halyard.Pool{}}
	c, h, err := e.halyardClient(ctx)
	if err != nil {
		return info
	}
	info.Configured = true
	if a, err := halyard.LocalAddr(h.URL); err == nil {
		info.LocalAddr = a
	}
	vips, err := c.VIPs(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	pools, err := c.Pools(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.VIPs, info.Pools = vips, pools
	if cf, err := c.Cloudflare(ctx); err == nil {
		info.Cloudflare = &cf
	}
	for _, p := range pools {
		if p.ID == h.PoolID {
			info.PoolOK = true
			if !e.healthFixed.Load() {
				fixed, err := c.FixHealthHost(ctx, p.ID)
				if err == nil {
					e.healthFixed.Store(true)
				}
				if fixed {
					e.Store.AddEvent(ctx, "info", "halyard", "", "Fixed the Halyard pool's health check (it now sends a Host header)")
				}
			}
		}
	}
	return info
}

// CreateHalyardPool adds a pool on Halyard that points at this server and
// makes it the pool every site uses.
func (e *Engine) CreateHalyardPool(ctx context.Context, addr string) (halyard.Pool, error) {
	c, h, err := e.halyardClient(ctx)
	if err != nil {
		return halyard.Pool{}, err
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		if addr, err = halyard.LocalAddr(h.URL); err != nil {
			return halyard.Pool{}, fmt.Errorf("could not work out this server's address: %w", err)
		}
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "80")
	}
	// Halyard's HAProxy may not resolve this server's name, so give it an IP.
	if host, port, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host) == nil {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			return halyard.Pool{}, fmt.Errorf("could not resolve %s; enter this server's IP address instead", host)
		}
		addr = net.JoinHostPort(ips[0].String(), port)
	}
	name := "berth"
	if e.Cfg.Name != "" {
		name = "berth-" + e.Cfg.Name
	}
	p, err := c.CreatePool(ctx, name, name, addr)
	if err != nil {
		return p, err
	}
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return p, err
	}
	st.Halyard.PoolID = p.ID
	return p, e.Store.SetSettings(ctx, st)
}

// pushHalyard creates or updates the site's Halyard host and, when asked,
// its certificate. It changes s.Halyard but does not save the site.
func (e *Engine) pushHalyard(ctx context.Context, s *model.Site, logf Logf) error {
	c, hs, err := e.halyardClient(ctx)
	if err != nil {
		return err
	}
	if hs.PoolID == "" {
		return errors.New("choose the Halyard pool for this server in Settings")
	}
	link := &s.Halyard
	if link.VIPID == "" {
		link.VIPID = hs.VIPID
	}
	if link.VIPID == "" {
		return errors.New("choose a Halyard VIP for this site, or a default one in Settings")
	}
	if link.Exposure == "" {
		link.Exposure = hs.Exposure
	}
	if link.Exposure == "" {
		link.Exposure = "public"
	}

	host := halyard.Host{}
	isNew := true
	if link.HostID != "" {
		h, err := c.GetHost(ctx, link.HostID)
		switch {
		case errors.Is(err, halyard.ErrNotFound):
			logf("The Halyard host was deleted; creating it again")
			link.HostID = ""
		case err != nil:
			return err
		default:
			host, isNew = h, false
		}
	}
	names := s.Hostnames()
	if isNew {
		h, err := findExistingHost(ctx, c, names)
		if err != nil {
			return err
		}
		if h != nil {
			// Take over the host as it is: only the backend changes, so the
			// VIP, exposure, DNS and certificate stay as they were.
			host, isNew = h, false
			link.HostID, _ = h["id"].(string)
			if v, _ := h["vip_id"].(string); v != "" {
				link.VIPID = v
			}
			if x, _ := h["exposure"].(string); x != "" {
				link.Exposure = x
			}
			if dns, ok := h["publish_dns"].(bool); ok {
				link.NoDNS = !dns
			}
			logf("Halyard already has a host for %s; switching it to this server", strings.Join(names, ", "))
		}
	}
	host["hostnames"] = names
	host["vip_id"] = link.VIPID
	host["mode"] = "http"
	host["exposure"] = link.Exposure
	if p, _ := host["http_port"].(float64); p == 0 || isNew {
		host["http_port"] = 80
	}
	if s.HTTPS {
		if p, _ := host["https_port"].(float64); p == 0 {
			host["https_port"] = 443
		}
		host["force_https"] = true
	} else {
		host["https_port"] = 0
		host["force_https"] = false
		host["hsts"] = false
		host["require_aop"] = false
	}
	host["routes"] = withDefaultRoute(host["routes"], hs.PoolID)
	// Public and tunnel hosts get proxied (orange cloud) records; LAN hosts
	// get a plain record pointing at the VIP.
	host["publish_dns"] = !link.NoDNS
	if isNew {
		host["enabled"] = true
		host["notes"] = "Managed by Berth (site " + s.Name + ")"
	}

	// Certificates: Halyard picks one by SNI, so a host only needs one to
	// exist. Berth asks for one when none covers the site's names.
	if hs.AutoCert && s.HTTPS {
		certs, err := c.Certs(ctx)
		if err != nil {
			return err
		}
		covered := false
		for _, ct := range certs {
			// A failed certificate Berth asked for is retried by Halyard, not replaced.
			if (ct.ID == link.CertID || ct.Status != "failed") && halyard.Covers(ct.Domains, names) {
				covered = true
				break
			}
		}
		if !covered {
			logf("Requesting a certificate on Halyard for %s", strings.Join(names, ", "))
			ct, err := c.CreateCert(ctx, names)
			if err != nil {
				return fmt.Errorf("certificate: %w", err)
			}
			old := link.CertID
			link.CertID = ct.ID
			if old != "" && old != ct.ID {
				defer e.dropUnusedCert(ctx, c, old, logf)
			}
		}
		// A certificate pinned to the host must still cover its names.
		if id, _ := host["certificate_id"].(string); id != "" {
			for _, ct := range certs {
				if ct.ID == id && !halyard.Covers(ct.Domains, names) {
					host["certificate_id"] = ""
				}
			}
		}
	}

	if isNew {
		logf("Creating Halyard host for %s", s.Domain)
	} else {
		logf("Updating Halyard host for %s", s.Domain)
	}
	saved, err := c.SaveHost(ctx, link.HostID, host)
	if err != nil {
		return err
	}
	if id, _ := saved["id"].(string); id != "" {
		link.HostID = id
	}
	return nil
}

// findExistingHost returns the Halyard host that serves exactly names, or nil
// when no host serves any of them. A host that shares only some names, or has
// others too, is an error: taking it over would move or drop sites that are
// not this one.
func findExistingHost(ctx context.Context, c *halyard.Client, names []string) (halyard.Host, error) {
	hosts, err := c.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	var match halyard.Host
	for _, h := range hosts {
		list, _ := h["hostnames"].([]any)
		var shared, extra []string
		for _, v := range list {
			n, _ := v.(string)
			if want[strings.ToLower(n)] {
				shared = append(shared, n)
			} else {
				extra = append(extra, n)
			}
		}
		if len(shared) == 0 {
			continue
		}
		hostName := strings.Join(shared, ", ")
		if len(extra) > 0 {
			return nil, fmt.Errorf("Halyard host %s also serves %s; remove those names from it or add them to this site, then publish again", hostName, strings.Join(extra, ", "))
		}
		if len(shared) < len(want) {
			var missing []string
			for _, n := range names {
				if !slices.ContainsFunc(shared, func(x string) bool { return strings.EqualFold(x, n) }) {
					missing = append(missing, n)
				}
			}
			return nil, fmt.Errorf("Halyard host %s does not serve %s; add those names to it or remove them from this site, then publish again", hostName, strings.Join(missing, ", "))
		}
		if match != nil {
			return nil, fmt.Errorf("more than one Halyard host serves %s", strings.Join(names, ", "))
		}
		match = h
	}
	return match, nil
}

// withDefaultRoute keeps any extra routes and points the default route at pool.
func withDefaultRoute(v any, pool string) []any {
	routes, _ := v.([]any)
	out := []any{}
	found := false
	for _, r := range routes {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if p, _ := m["path_prefix"].(string); p == "" {
			if found {
				continue
			}
			found = true
			m["pool_id"] = pool
		}
		out = append(out, m)
	}
	if !found {
		out = append(out, map[string]any{"path_prefix": "", "pool_id": pool})
	}
	return out
}

// dropUnusedCert deletes a certificate Berth requested once no host uses it.
func (e *Engine) dropUnusedCert(ctx context.Context, c *halyard.Client, id string, logf Logf) {
	certs, err := c.Certs(ctx)
	if err != nil {
		return
	}
	for _, ct := range certs {
		if ct.ID == id && ct.Hosts == 0 {
			if err := c.DeleteCert(ctx, id); err != nil {
				logf("Could not delete the old Halyard certificate: %v", err)
			} else {
				logf("Deleted the Halyard certificate for %s", strings.Join(ct.Domains, ", "))
			}
		}
	}
}

// syncHalyard creates or updates the site's host on Halyard when the site is
// published there. Failures are recorded on the site, never fatal.
func (e *Engine) syncHalyard(ctx context.Context, s *model.Site, logf Logf) {
	if !s.Halyard.Enabled {
		return
	}
	err := e.pushHalyard(ctx, s, logf)
	e.saveHalyardLink(ctx, s, err, logf)
}

func (e *Engine) saveHalyardLink(ctx context.Context, s *model.Site, err error, logf Logf) {
	s.Halyard.Error = ""
	if err != nil {
		s.Halyard.Error = firstLine(err.Error())
		logf("Halyard: %v", err)
		e.Store.AddEvent(ctx, "error", "halyard", s.Name, "Could not update Halyard for "+s.Domain+": "+s.Halyard.Error)
	}
	// Save only the link: other fields may have changed meanwhile.
	if cur, gerr := e.Store.Site(ctx, s.Name); gerr == nil {
		cur.Halyard = s.Halyard
		_ = e.Store.SaveSite(ctx, cur)
	}
}

// unlinkHalyard removes the site's host from Halyard, and the certificate
// Berth requested for it when nothing else uses it.
func (e *Engine) unlinkHalyard(ctx context.Context, s *model.Site, logf Logf) error {
	if s.Halyard.HostID == "" && s.Halyard.CertID == "" {
		return nil
	}
	c, _, err := e.halyardClient(ctx)
	if err != nil {
		logf("Halyard: %v", err)
		return err
	}
	if s.Halyard.HostID != "" {
		logf("Removing the Halyard host for %s", s.Domain)
		if err := c.DeleteHost(ctx, s.Halyard.HostID); err != nil {
			logf("Halyard: %v", err)
			return err
		}
	}
	if s.Halyard.CertID != "" {
		e.dropUnusedCert(ctx, c, s.Halyard.CertID, logf)
	}
	return nil
}

func validExposure(x string) bool {
	switch x {
	case "", "lan", "public", "tunnel":
		return true
	}
	return false
}

// LinkHalyard publishes a site on Halyard, or updates its host.
func (e *Engine) LinkHalyard(ctx context.Context, name string, l model.HalyardLink, author string) (model.Site, error) {
	unlock := e.lockSite(name)
	defer unlock()
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return s, err
	}
	if !validExposure(l.Exposure) {
		return s, fmt.Errorf("unknown exposure %q", l.Exposure)
	}
	s.Halyard.Enabled = true
	if l.VIPID != "" {
		s.Halyard.VIPID = l.VIPID
	}
	if l.Exposure != "" {
		s.Halyard.Exposure = l.Exposure
	}
	s.Halyard.NoDNS = l.NoDNS
	logf := func(string, ...any) {}
	err = e.pushHalyard(ctx, &s, logf)
	e.saveHalyardLink(ctx, &s, err, logf)
	if err == nil {
		e.Store.AddEvent(ctx, "info", "halyard", s.Name, author+" published "+s.Domain+" on Halyard")
	}
	return s, err
}

// UnlinkHalyard removes a site from Halyard.
func (e *Engine) UnlinkHalyard(ctx context.Context, name string, author string) (model.Site, error) {
	unlock := e.lockSite(name)
	defer unlock()
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return s, err
	}
	if err := e.unlinkHalyard(ctx, &s, func(string, ...any) {}); err != nil {
		return s, err
	}
	s.Halyard = model.HalyardLink{}
	if err := e.Store.SaveSite(ctx, s); err != nil {
		return s, err
	}
	e.Store.AddEvent(ctx, "info", "halyard", s.Name, author+" removed "+s.Domain+" from Halyard")
	return s, nil
}
