package engine

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/joshuadouch/berth/internal/model"
)

// Let's Encrypt's directories.
const (
	letsEncryptProd    = "https://acme-v02.api.letsencrypt.org/directory"
	letsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// renewBefore is how long before expiry a certificate is renewed.
const renewBefore = 30 * 24 * time.Hour

// acmeDirectory returns the ACME server to use.
func (e *Engine) acmeDirectory(st model.Settings) string {
	switch {
	case e.ACMEDirectory != "":
		return e.ACMEDirectory
	case st.ACME.Staging:
		return letsEncryptStaging
	}
	return letsEncryptProd
}

// acmeKey loads (or creates) the ACME account key.
func (e *Engine) acmeKey() (crypto.Signer, error) {
	p := e.Cfg.Path("acme", "account.key")
	if b, err := os.ReadFile(p); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("%s is not a PEM key", p)
		}
		return x509.ParseECPrivateKey(blk.Bytes)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return key, writeFileAtomic(p, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

// acmeClient returns a client registered with the ACME server.
func (e *Engine) acmeClient(ctx context.Context) (*acme.Client, error) {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	key, err := e.acmeKey()
	if err != nil {
		return nil, fmt.Errorf("ACME account key: %w", err)
	}
	dir := e.acmeDirectory(st)
	c := &acme.Client{Key: key, DirectoryURL: dir, UserAgent: "berth/" + e.Version, HTTPClient: e.ACMEHTTP}
	sum := sha256.Sum256([]byte(dir))
	marker := e.Cfg.Path("acme", "registered-"+hex.EncodeToString(sum[:6]))
	if _, err := os.Stat(marker); err == nil {
		return c, nil
	}
	acct := &acme.Account{}
	if email := e.acmeEmail(ctx, st); email != "" {
		acct.Contact = []string{"mailto:" + email}
	}
	if _, err := c.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("registering with Let's Encrypt: %w", err)
	}
	_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
	return c, nil
}

// acmeEmail is the contact for expiry warnings.
func (e *Engine) acmeEmail(ctx context.Context, st model.Settings) string {
	if st.ACME.Email != "" {
		return st.ACME.Email
	}
	if u, err := e.Store.FirstUser(ctx); err == nil {
		return u.Email
	}
	return ""
}

// challengeFile is where nginx serves an HTTP-01 token from.
func (e *Engine) challengeFile(token string) string {
	return filepath.Join(e.Cfg.ACMEDir, ".well-known", "acme-challenge", token)
}

func (e *Engine) writeChallenge(token, body string) error {
	p := e.challengeFile(token)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// The challenge directory must stay readable by nginx whatever Berth's umask.
	for d := filepath.Dir(p); strings.HasPrefix(d, e.Cfg.ACMEDir); d = filepath.Dir(d) {
		_ = os.Chmod(d, 0o755)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Chmod(p, 0o644)
}

// preflight checks that every name reaches this server over plain HTTP
// before asking Let's Encrypt, which limits failed attempts.
func (e *Engine) preflight(ctx context.Context, names []string) error {
	if e.SkipACMEPreflight {
		return nil
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	token := "berth-check-" + hex.EncodeToString(b)
	want := hex.EncodeToString(b)
	if err := e.writeChallenge(token, want); err != nil {
		return err
	}
	defer os.Remove(e.challengeFile(token))
	var bad []string
	for _, n := range names {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+n+"/.well-known/acme-challenge/"+token, nil)
		res, err := e.httpClient(15 * time.Second).Do(req)
		if err != nil {
			bad = append(bad, n)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		res.Body.Close()
		if res.StatusCode != 200 || strings.TrimSpace(string(body)) != want {
			bad = append(bad, n)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s do%s not reach this server over HTTP yet; point %s A/AAAA records at this server's public address (with port 80 open) and try again",
			strings.Join(bad, ", "), map[bool]string{true: "es", false: ""}[len(bad) == 1], map[bool]string{true: "its", false: "their"}[len(bad) == 1])
	}
	return nil
}

// IssueCert gets a certificate for every hostname of a site and stores it in
// the site's certificate directory.
func (e *Engine) IssueCert(ctx context.Context, s model.Site, logf Logf) (model.SiteTLS, error) {
	names := s.Hostnames()
	if e.Cfg.DryRun {
		logf("Dry run: not asking Let's Encrypt for %s", strings.Join(names, ", "))
		return model.SiteTLS{}, errors.New("dry run")
	}
	logf("Checking that %s reach this server", strings.Join(names, ", "))
	if err := e.preflight(ctx, names); err != nil {
		return model.SiteTLS{}, err
	}
	c, err := e.acmeClient(ctx)
	if err != nil {
		return model.SiteTLS{}, err
	}
	logf("Asking Let's Encrypt for a certificate")
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return model.SiteTLS{}, explainACME(err)
	}
	for _, u := range order.AuthzURLs {
		z, err := c.GetAuthorization(ctx, u)
		if err != nil {
			return model.SiteTLS{}, explainACME(err)
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range z.Challenges {
			if ch.Type == "http-01" {
				chal = ch
			}
		}
		if chal == nil {
			return model.SiteTLS{}, fmt.Errorf("Let's Encrypt offered no HTTP check for %s", z.Identifier.Value)
		}
		body, err := c.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return model.SiteTLS{}, err
		}
		if err := e.writeChallenge(chal.Token, body); err != nil {
			return model.SiteTLS{}, err
		}
		defer os.Remove(e.challengeFile(chal.Token))
		logf("Proving control of %s", z.Identifier.Value)
		if _, err := c.Accept(ctx, chal); err != nil {
			return model.SiteTLS{}, explainACME(err)
		}
		if _, err := c.WaitAuthorization(ctx, z.URI); err != nil {
			return model.SiteTLS{}, fmt.Errorf("%s: %w", z.Identifier.Value, explainACME(err))
		}
	}
	finalize := order.FinalizeURL
	if o, err := c.WaitOrder(ctx, order.URI); err != nil {
		return model.SiteTLS{}, explainACME(err)
	} else if o.FinalizeURL != "" {
		finalize = o.FinalizeURL
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return model.SiteTLS{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: names[0]}, DNSNames: names}, key)
	if err != nil {
		return model.SiteTLS{}, err
	}
	ders, _, err := c.CreateOrderCert(ctx, finalize, csr, true)
	if err != nil {
		// Some CAs answer the finalize request without the order's Location,
		// which the library needs; the order itself still finishes.
		o, werr := c.WaitOrder(ctx, order.URI)
		if werr != nil || o.CertURL == "" {
			return model.SiteTLS{}, explainACME(err)
		}
		if ders, err = c.FetchCert(ctx, o.CertURL, true); err != nil {
			return model.SiteTLS{}, explainACME(err)
		}
	}
	if len(ders) == 0 {
		return model.SiteTLS{}, errors.New("Let's Encrypt returned no certificate")
	}
	leaf, err := x509.ParseCertificate(ders[0])
	if err != nil {
		return model.SiteTLS{}, err
	}
	var chain []byte
	for _, d := range ders {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return model.SiteTLS{}, err
	}
	p := e.Paths()
	if err := os.MkdirAll(filepath.Dir(p.CertFile(s.Name)), 0o700); err != nil {
		return model.SiteTLS{}, err
	}
	if err := writeFileAtomic(p.KeyFile(s.Name), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return model.SiteTLS{}, err
	}
	if err := writeFileAtomic(p.CertFile(s.Name), chain, 0o644); err != nil {
		return model.SiteTLS{}, err
	}
	logf("Certificate issued for %s, valid until %s", strings.Join(names, ", "), leaf.NotAfter.Format("2 Jan 2006"))
	return model.SiteTLS{Status: model.TLSActive, Domains: names, Issued: time.Now().UTC(), Expires: leaf.NotAfter.UTC()}, nil
}

// explainACME turns ACME problems into a readable line.
func explainACME(err error) error {
	var ae *acme.Error
	if errors.As(err, &ae) {
		msg := ae.Detail
		if msg == "" {
			msg = ae.ProblemType
		}
		if strings.Contains(ae.ProblemType, "rateLimited") {
			msg = "Let's Encrypt rate limit: " + msg
		}
		return errors.New(msg)
	}
	var oe *acme.OrderError
	if errors.As(err, &oe) {
		return fmt.Errorf("the order ended as %s", oe.Status)
	}
	return err
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// certDue reports whether a site needs a (new) certificate now.
func certDue(s model.Site, now time.Time) bool {
	if !s.LocalTLS() || s.Status != model.StatusActive {
		return false
	}
	if s.TLS.NextTry.After(now) {
		return false
	}
	return s.TLS.Status != model.TLSActive || !s.TLSCovers() || s.TLS.Expires.Sub(now) < renewBefore
}

// EnsureCert issues or renews a site's certificate when it is due (or always
// with force), saves the result and applies the nginx config.
func (e *Engine) EnsureCert(ctx context.Context, name string, force bool, logf Logf) error {
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return err
	}
	if !s.LocalTLS() {
		return errors.New("Berth does not end HTTPS for this site")
	}
	if !force && !certDue(s, time.Now()) {
		logf("The certificate is up to date")
		return nil
	}
	tls, ierr := e.IssueCert(ctx, s, logf)
	unlock := e.lockSite(name)
	cur, err := e.Store.Site(ctx, name)
	if err != nil {
		unlock()
		return err
	}
	if ierr != nil {
		cur.TLS.Fails++
		cur.TLS.Error = firstLine(ierr.Error())
		backoff := time.Hour << min(cur.TLS.Fails-1, 5)
		if backoff > 24*time.Hour {
			backoff = 24 * time.Hour
		}
		cur.TLS.NextTry = time.Now().Add(backoff).UTC()
		if cur.TLS.Status != model.TLSActive {
			cur.TLS.Status = model.TLSError
		}
		_ = e.Store.SaveSite(ctx, cur)
		unlock()
		e.Store.AddEvent(ctx, "error", "tls", name, "Could not get a certificate for "+cur.Domain+": "+cur.TLS.Error)
		return ierr
	}
	cur.TLS = tls
	_ = e.Store.SaveSite(ctx, cur)
	unlock()
	e.Store.AddEvent(ctx, "info", "tls", name, "Issued a Let's Encrypt certificate for "+strings.Join(tls.Domains, ", "))
	return e.Apply(ctx)
}

// RequestCert starts a job that gets a site's certificate now.
func (e *Engine) RequestCert(ctx context.Context, name string) (int64, error) {
	s, err := e.Store.Site(ctx, name)
	if err != nil {
		return 0, err
	}
	if !s.LocalTLS() {
		return 0, errors.New("Berth does not end HTTPS for this site")
	}
	return e.StartJob(name, "tls", "Certificate for "+s.Domain, func(ctx context.Context, logf Logf) error {
		return e.EnsureCert(ctx, name, true, logf)
	})
}

// renewCerts issues and renews certificates that are due, one at a time.
func (e *Engine) renewCerts(ctx context.Context) {
	st, err := e.Store.Settings(ctx)
	if err != nil || st.ACME.Off || e.Cfg.DryRun {
		return
	}
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, s := range sites {
		if !certDue(s, now) {
			continue
		}
		_ = e.EnsureCert(ctx, s.Name, false, func(f string, a ...any) { e.Log.Info("tls: "+fmt.Sprintf(f, a...), "site", s.Name) })
	}
}
