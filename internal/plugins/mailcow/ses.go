package mailcow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// SES talks to the Amazon SES v2 API with SigV4-signed requests.
type SES struct {
	Region    string
	AccessKey string
	SecretKey string
	Endpoint  string // tests; default https://email.<region>.amazonaws.com
	HTTP      *http.Client
	now       func() time.Time
}

// SESError is an error answer from SES.
type SESError struct {
	Status int
	Type   string
	Msg    string
}

func (e *SESError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("Amazon SES: %s: %s", e.Type, e.Msg)
	}
	return fmt.Sprintf("Amazon SES: HTTP %d: %s", e.Status, e.Msg)
}

func isSESType(err error, t string) bool {
	var e *SESError
	return errors.As(err, &e) && (e.Type == t || (t == "NotFoundException" && e.Status == 404) || (t == "AlreadyExistsException" && e.Status == 409))
}

func (s *SES) endpoint() string {
	if s.Endpoint != "" {
		return strings.TrimRight(s.Endpoint, "/")
	}
	return "https://email." + s.Region + ".amazonaws.com"
}

func (s *SES) do(ctx context.Context, method, path string, body, out any) error {
	if s.Region == "" || s.AccessKey == "" || s.SecretKey == "" {
		return errors.New("set the Amazon SES region and access keys on the Plugins page")
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.endpoint()+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	sign(req, payload, s.AccessKey, s.SecretKey, s.Region, "ses", now())
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("Amazon SES: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		e := &SESError{Status: res.StatusCode, Type: res.Header.Get("X-Amzn-Errortype")}
		var m struct {
			Message  string `json:"message"`
			Message2 string `json:"Message"`
		}
		_ = json.Unmarshal(b, &m)
		e.Msg = m.Message + m.Message2
		if e.Msg == "" {
			e.Msg = short(b)
		}
		if i := strings.IndexByte(e.Type, ':'); i > 0 {
			e.Type = e.Type[:i]
		}
		return e
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Account is the SES account status in the region.
type Account struct {
	SendingEnabled          bool   `json:"SendingEnabled"`
	ProductionAccessEnabled bool   `json:"ProductionAccessEnabled"`
	EnforcementStatus       string `json:"EnforcementStatus"`
	SendQuota               struct {
		Max24HourSend   float64 `json:"Max24HourSend"`
		MaxSendRate     float64 `json:"MaxSendRate"`
		SentLast24Hours float64 `json:"SentLast24Hours"`
	} `json:"SendQuota"`
}

// Account checks the keys and returns the account status.
func (s *SES) Account(ctx context.Context) (Account, error) {
	var a Account
	return a, s.do(ctx, "GET", "/v2/email/account", nil, &a)
}

// Identity is a verified (or verifying) SES domain.
type Identity struct {
	VerifiedForSending bool   `json:"VerifiedForSendingStatus"`
	VerificationStatus string `json:"VerificationStatus"`
	Dkim               struct {
		SigningEnabled bool     `json:"SigningEnabled"`
		Status         string   `json:"Status"`
		Tokens         []string `json:"Tokens"`
	} `json:"DkimAttributes"`
	MailFrom struct {
		Domain   string `json:"MailFromDomain"`
		Status   string `json:"MailFromDomainStatus"`
		OnFailMX string `json:"BehaviorOnMxFailure"`
	} `json:"MailFromAttributes"`
}

func identityPath(domain string) string {
	return "/v2/email/identities/" + url.PathEscape(domain)
}

// Identity loads a domain identity (an error matching NotFoundException when absent).
func (s *SES) Identity(ctx context.Context, domain string) (Identity, error) {
	var id Identity
	return id, s.do(ctx, "GET", identityPath(domain), nil, &id)
}

// EnsureIdentity creates the domain identity with Easy DKIM if needed and
// returns it.
func (s *SES) EnsureIdentity(ctx context.Context, domain string) (Identity, bool, error) {
	id, err := s.Identity(ctx, domain)
	if err == nil {
		return id, false, nil
	}
	if !isSESType(err, "NotFoundException") {
		return id, false, err
	}
	err = s.do(ctx, "POST", "/v2/email/identities", map[string]any{
		"EmailIdentity":         domain,
		"DkimSigningAttributes": map[string]any{"NextSigningKeyLength": "RSA_2048_BIT"},
	}, nil)
	if err != nil && !isSESType(err, "AlreadyExistsException") {
		return id, false, err
	}
	id, err = s.Identity(ctx, domain)
	return id, true, err
}

// SetMailFrom sets a custom MAIL FROM domain (blank removes it).
func (s *SES) SetMailFrom(ctx context.Context, domain, mailFrom string) error {
	body := map[string]any{"BehaviorOnMxFailure": "USE_DEFAULT_VALUE"}
	if mailFrom != "" {
		body["MailFromDomain"] = mailFrom
	}
	return s.do(ctx, "PUT", identityPath(domain)+"/mail-from", body, nil)
}

// DeleteIdentity removes a domain identity (absent is fine).
func (s *SES) DeleteIdentity(ctx context.Context, domain string) error {
	err := s.do(ctx, "DELETE", identityPath(domain), nil, nil)
	if isSESType(err, "NotFoundException") {
		return nil
	}
	return err
}

// SMTPHost is the SES SMTP endpoint mailcow relays through.
func SMTPHost(region string) string {
	return "email-smtp." + region + ".amazonaws.com:587"
}

// SMTPPassword derives the SES SMTP password from an IAM secret access key
// (the algorithm AWS documents for "Obtaining Amazon SES SMTP credentials").
func SMTPPassword(secret, region string) string {
	sig := hmacSHA([]byte("AWS4"+secret), "11111111")
	for _, s := range []string{region, "ses", "aws4_request", "SendRawEmail"} {
		sig = hmacSHA(sig, s)
	}
	return base64.StdEncoding.EncodeToString(append([]byte{0x04}, sig...))
}

func hmacSHA(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sign adds AWS Signature Version 4 headers to req.
func sign(req *http.Request, payload []byte, akid, secret, region, service string, t time.Time) {
	t = t.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if req.Header.Get("Host") == "" {
		req.Header.Set("Host", req.URL.Host)
	}

	names := []string{}
	for k := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
		}
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + strings.Join(strings.Fields(req.Header.Get(n)), " ") + "\n")
	}
	signed := strings.Join(names, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			qs = append(qs, awsEscape(k)+"="+awsEscape(v))
		}
	}
	canonical := strings.Join([]string{req.Method, path, strings.Join(qs, "&"), ch.String(), signed, sha256Hex(payload)}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := signingKey(secret, day, region, service)
	sig := hex.EncodeToString(hmacSHA(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+akid+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	req.Header.Del("Host") // net/http sends req.Host
}

func signingKey(secret, day, region, service string) []byte {
	k := hmacSHA([]byte("AWS4"+secret), day)
	k = hmacSHA(k, region)
	k = hmacSHA(k, service)
	return hmacSHA(k, "aws4_request")
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
