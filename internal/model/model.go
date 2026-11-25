// Package model holds the objects Berth stores and renders into nginx,
// PHP-FPM and MariaDB.
package model

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// SiteType is what a site runs.
type SiteType string

const (
	TypeWordPress SiteType = "wordpress" // WordPress with its own database
	TypePHPDB     SiteType = "php_db"    // PHP with its own database
	TypePHP       SiteType = "php"       // PHP without a database
)

// HasDB reports whether the site type gets a database.
func (t SiteType) HasDB() bool { return t == TypeWordPress || t == TypePHPDB }

// Label is the type's display name.
func (t SiteType) Label() string {
	switch t {
	case TypeWordPress:
		return "WordPress"
	case TypePHPDB:
		return "PHP + database"
	}
	return "PHP"
}

// Site statuses.
const (
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusError        = "error"
	StatusDeleting     = "deleting"
)

// PHPVersions Berth knows how to install (from packages.sury.org).
var PHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

// Database is a site's MariaDB database. Name and User are equal.
type Database struct {
	Name     string `json:"name"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
}

// WordPress holds what was used to install WordPress.
type WordPress struct {
	Title      string `json:"title"`
	AdminUser  string `json:"admin_user"`
	AdminEmail string `json:"admin_email"`
	Locale     string `json:"locale"`
}

// SFTP is per-site file access, chrooted to the site directory.
type SFTP struct {
	Enabled bool     `json:"enabled"`
	Keys    []string `json:"keys"`
	// PasswordSet is true once a password was set (the hash lives in /etc/shadow).
	PasswordSet bool `json:"password_set"`
}

// HalyardLink is the host Berth created on Halyard for this site.
type HalyardLink struct {
	Enabled  bool   `json:"enabled"`
	HostID   string `json:"host_id"`
	VIPID    string `json:"vip_id"`
	Exposure string `json:"exposure"` // lan, public, tunnel
	CertID   string `json:"cert_id"`
	// NoDNS leaves Cloudflare DNS alone for this site (DNS is managed by default).
	NoDNS bool   `json:"no_dns"`
	Error string `json:"error"`
	// MovedAt is when the site moved off Halyard to be served by Berth.
	MovedAt time.Time `json:"moved_at,omitzero"`
	// RemoveAt is when the Halyard host of a moved site is deleted (zero:
	// when someone presses Remove, because DNS still has to be changed).
	RemoveAt time.Time `json:"remove_at,omitzero"`
}

// Certificate states.
const (
	TLSNone    = ""
	TLSPending = "pending"
	TLSActive  = "active"
	TLSError   = "error"
)

// SiteTLS is a site's Let's Encrypt certificate.
type SiteTLS struct {
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Domains []string  `json:"domains,omitempty"` // names the certificate covers
	Issued  time.Time `json:"issued,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
	// Fails counts failed attempts in a row; NextTry backs off after them.
	Fails   int       `json:"fails,omitempty"`
	NextTry time.Time `json:"next_try,omitempty"`
}

// Who ends HTTPS for a site.
const (
	TLSModeProxy = ""      // a load balancer or Cloudflare in front (Halyard)
	TLSModeLocal = "local" // Berth, with a Let's Encrypt certificate
)

// LocalTLS reports whether Berth itself ends HTTPS for the site. Sites
// published on Halyard always leave TLS to Halyard.
func (s Site) LocalTLS() bool { return s.HTTPS && s.TLSMode == TLSModeLocal && !s.Halyard.Enabled }

// ServesTLS reports whether nginx serves the site on port 443 with its
// certificate.
func (s Site) ServesTLS() bool { return s.LocalTLS() && s.TLS.Status == TLSActive }

// TLSCovers reports whether the certificate covers every hostname.
func (s Site) TLSCovers() bool {
	have := map[string]bool{}
	for _, d := range s.TLS.Domains {
		have[d] = true
	}
	for _, h := range s.Hostnames() {
		if !have[h] {
			return false
		}
	}
	return len(s.TLS.Domains) > 0
}

// SiteImport names the staged uploads a new site is built from.
type SiteImport struct {
	Files     string `json:"files"` // upload id of a .zip/.tar(.gz)
	FilesName string `json:"files_name"`
	DB        string `json:"db"` // upload id of a .sql(.gz)
	DBName    string `json:"db_name"`
}

// Site is one website.
type Site struct {
	ID   string `json:"id"`   // equal to Name
	Name string `json:"name"` // short name: Linux user, database and directory
	// AccountID is the account that owns the site.
	AccountID  string   `json:"account_id"`
	Type       SiteType `json:"type"`
	Domain     string   `json:"domain"`
	Aliases    []string `json:"aliases"`
	PHPVersion string   `json:"php_version"`
	// DocRoot is relative to the site's public directory ("" serves public).
	DocRoot string `json:"doc_root"`
	// RedirectAliases sends visitors on an alias to the main domain.
	RedirectAliases bool `json:"redirect_aliases"`
	// FrontController sends requests for missing files to /index.php.
	FrontController bool `json:"front_controller"`
	// HTTPS means visitors reach the site over HTTPS (TLS ends at Halyard).
	HTTPS bool `json:"https"`
	// TLSMode says who ends HTTPS: a proxy in front ("") or Berth ("local").
	TLSMode     string      `json:"tls_mode"`
	Enabled     bool        `json:"enabled"`
	UploadMB    int         `json:"upload_mb"`
	MemoryMB    int         `json:"memory_mb"`
	MaxChildren int         `json:"max_children"`
	Backups     bool        `json:"backups"`
	DB          *Database   `json:"db,omitempty"`
	WP          *WordPress  `json:"wp,omitempty"`
	SFTP        SFTP        `json:"sftp"`
	Halyard     HalyardLink `json:"halyard"`
	// Import is the uploaded site a new site is created from.
	Import      *SiteImport `json:"import,omitempty"`
	Status      string      `json:"status"`
	StatusError string      `json:"status_error"`
	Notes       string      `json:"notes"`
	// NginxCustom is extra nginx directives for the site's server block.
	NginxCustom string `json:"nginx_custom,omitempty"`
	// PHP holds extra PHP settings for the site's FPM pool.
	PHP PHPSettings `json:"php"`
	// AutoUpdate updates WordPress on a schedule.
	AutoUpdate AutoUpdate `json:"auto_update"`
	// NoMonitor turns off uptime checks for the site.
	NoMonitor bool `json:"no_monitor"`
	// TLS is the site's own certificate, used when Berth ends HTTPS itself.
	TLS       SiteTLS   `json:"tls"`
	CreatedAt time.Time `json:"created_at"`
	// NoShell is set while rendering for sites whose plan blocks PHP's shell
	// functions; it is never stored.
	NoShell bool `json:"-"`
}

// PHPSettings are per-site PHP options beyond memory and uploads.
type PHPSettings struct {
	MaxExecution int `json:"max_execution"`  // seconds, default 300
	MaxInputVars int `json:"max_input_vars"` // default 5000
	MaxInputTime int `json:"max_input_time"` // seconds, default 300 (-1 = max_execution)
	// Custom is extra php.ini lines ("key = value"), one per line.
	Custom string `json:"custom,omitempty"`
}

// AutoUpdate is a WordPress site's update schedule.
type AutoUpdate struct {
	Enabled bool   `json:"enabled"`
	Day     string `json:"day"`  // "daily" or mon..sun
	Time    string `json:"time"` // HH:MM local time
	Core    bool   `json:"core"`
	Plugins bool   `json:"plugins"`
	Themes  bool   `json:"themes"`
	// Rollback restores the pre-update backup when the site breaks.
	Rollback bool `json:"rollback"`
}

// AutoUpdateDays are the accepted values of AutoUpdate.Day.
var AutoUpdateDays = []string{"daily", "mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// PHPManagedKeys are php.ini settings Berth sets for isolation; custom lines
// cannot change them.
var PHPManagedKeys = map[string]bool{
	"open_basedir": true, "upload_tmp_dir": true, "sys_temp_dir": true, "session.save_path": true,
	"error_log": true, "memory_limit": true, "upload_max_filesize": true, "post_max_size": true,
	"max_execution_time": true, "max_input_vars": true, "max_input_time": true,
	"disable_functions": true, "disable_classes": true, "extension": true, "zend_extension": true, "extension_dir": true,
}

var iniKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,79}$`)

// INILine is one custom php.ini setting.
type INILine struct{ Key, Value string }

// ParsePHPCustom parses custom php.ini lines. Blank lines and ; or # comments
// are skipped.
func ParsePHPCustom(text string) ([]INILine, error) {
	var out []INILine
	for i, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !iniKeyRe.MatchString(k) {
			return nil, fmt.Errorf("PHP settings line %d should look like key = value", i+1)
		}
		if PHPManagedKeys[k] {
			return nil, fmt.Errorf("PHP setting %s is set by Berth; use the fields above", k)
		}
		v = strings.Trim(v, `"'`)
		if strings.ContainsAny(v, "\x00\n\"") || len(v) > 1024 {
			return nil, fmt.Errorf("PHP setting %s has a value Berth cannot write", k)
		}
		out = append(out, INILine{k, v})
	}
	return out, nil
}

// WithDefaults fills unset values (sites saved before these settings existed).
func (p PHPSettings) WithDefaults() PHPSettings {
	_ = p.normalize()
	return p
}

func (p *PHPSettings) normalize() error {
	if p.MaxExecution <= 0 {
		p.MaxExecution = 300
	}
	if p.MaxExecution > 3600 {
		return fmt.Errorf("max execution time is at most 3600 seconds")
	}
	if p.MaxInputVars <= 0 {
		p.MaxInputVars = 5000
	}
	if p.MaxInputVars > 100000 {
		return fmt.Errorf("max input vars is at most 100000")
	}
	if p.MaxInputTime == 0 {
		p.MaxInputTime = 300
	}
	if p.MaxInputTime < -1 || p.MaxInputTime > 3600 {
		return fmt.Errorf("max input time must be -1 or up to 3600 seconds")
	}
	p.Custom = strings.TrimSpace(p.Custom)
	if len(p.Custom) > 16<<10 {
		return fmt.Errorf("custom PHP settings are limited to 16 KB")
	}
	_, err := ParsePHPCustom(p.Custom)
	return err
}

func (a *AutoUpdate) normalize() error {
	if a.Day == "" {
		a.Day = "daily"
	}
	ok := false
	for _, d := range AutoUpdateDays {
		ok = ok || d == a.Day
	}
	if !ok {
		return fmt.Errorf("unknown update day %q", a.Day)
	}
	if a.Time == "" {
		a.Time = "04:00"
	}
	if !timeRe.MatchString(a.Time) {
		return fmt.Errorf("update time must look like 04:00")
	}
	if a.Enabled && !a.Core && !a.Plugins && !a.Themes {
		return fmt.Errorf("choose what automatic updates should update")
	}
	return nil
}

// Due reports whether the schedule runs at t (to the minute).
func (a AutoUpdate) Due(t time.Time) bool {
	if !a.Enabled || t.Format("15:04") != a.Time {
		return false
	}
	return a.Day == "daily" || strings.ToLower(t.Format("Mon")) == a.Day
}

// User is the Linux user that owns the site's files and runs its PHP.
func (s Site) User() string { return "site_" + s.Name }

// Hostnames is the domain followed by the aliases.
func (s Site) Hostnames() []string { return append([]string{s.Domain}, s.Aliases...) }

// URL is the address visitors use.
func (s Site) URL() string {
	if s.HTTPS {
		return "https://" + s.Domain
	}
	return "http://" + s.Domain
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9]{1,23}$`)
	domainRe  = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}$`)
	docRootRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	localeRe  = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?(_[a-z]+)?$`)
	keyRe     = regexp.MustCompile(`^(ssh-(rsa|ed25519|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/=]+( [^\r\n]*)?$`)
)

// ValidName reports whether n can be a site name.
func ValidName(n string) bool { return nameRe.MatchString(n) }

// NameFromDomain suggests a site name for a domain: www.example.co.uk -> exampleco.
func NameFromDomain(d string) string {
	d = strings.TrimPrefix(strings.ToLower(d), "www.")
	var b strings.Builder
	for _, r := range d {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	n := b.String()
	for len(n) > 0 && n[0] >= '0' && n[0] <= '9' {
		n = n[1:]
	}
	if len(n) > 24 {
		n = n[:24]
	}
	if len(n) < 2 {
		n = "site" + n
	}
	return n
}

// ValidDomain reports whether d is a hostname nginx can serve.
func ValidDomain(d string) bool {
	return len(d) <= 253 && domainRe.MatchString(d) && net.ParseIP(d) == nil
}

// SupportedPHP reports whether v is a PHP version Berth knows.
func SupportedPHP(v string) bool {
	for _, x := range PHPVersions {
		if x == v {
			return true
		}
	}
	return false
}

// Normalize fills defaults and validates the site.
func (s *Site) Normalize() error {
	s.Name = strings.ToLower(strings.TrimSpace(s.Name))
	if !ValidName(s.Name) {
		return fmt.Errorf("name must be 2-24 lowercase letters or digits, starting with a letter")
	}
	s.ID = s.Name
	if s.TLSMode != TLSModeProxy && s.TLSMode != TLSModeLocal {
		return fmt.Errorf("unknown TLS mode %q", s.TLSMode)
	}
	switch s.Type {
	case TypeWordPress, TypePHPDB, TypePHP:
	default:
		return fmt.Errorf("unknown site type %q", s.Type)
	}
	s.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Domain)), ".")
	if !ValidDomain(s.Domain) || strings.HasPrefix(s.Domain, "*.") {
		return fmt.Errorf("%q is not a valid domain", s.Domain)
	}
	seen := map[string]bool{s.Domain: true}
	var al []string
	for _, a := range s.Aliases {
		a = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a)), ".")
		if a == "" || seen[a] {
			continue
		}
		if !ValidDomain(a) {
			return fmt.Errorf("%q is not a valid domain", a)
		}
		seen[a] = true
		al = append(al, a)
	}
	s.Aliases = al
	if !SupportedPHP(s.PHPVersion) {
		return fmt.Errorf("PHP %q is not supported", s.PHPVersion)
	}
	s.DocRoot = strings.Trim(strings.TrimSpace(s.DocRoot), "/")
	if s.DocRoot != "" && (!docRootRe.MatchString(s.DocRoot) || strings.Contains("/"+s.DocRoot+"/", "/../") || strings.Contains("/"+s.DocRoot+"/", "/./")) {
		return fmt.Errorf("document root must be a folder inside public, like web or public/dist")
	}
	if s.UploadMB <= 0 {
		s.UploadMB = 64
	}
	if s.UploadMB > 4096 {
		return fmt.Errorf("upload limit is at most 4096 MB")
	}
	if s.MemoryMB <= 0 {
		s.MemoryMB = 256
	}
	if s.MemoryMB < 32 || s.MemoryMB > 8192 {
		return fmt.Errorf("memory limit must be 32-8192 MB")
	}
	if s.MaxChildren <= 0 {
		s.MaxChildren = 5
	}
	if s.MaxChildren > 200 {
		return fmt.Errorf("PHP workers is at most 200")
	}
	if s.WP != nil && s.WP.Locale != "" && !localeRe.MatchString(s.WP.Locale) {
		return fmt.Errorf("language %q should look like en_US", s.WP.Locale)
	}
	var keys []string
	for _, k := range s.SFTP.Keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !keyRe.MatchString(k) {
			return fmt.Errorf("SSH key %.30q… is not an OpenSSH public key", k)
		}
		keys = append(keys, k)
	}
	s.SFTP.Keys = keys
	if err := s.PHP.normalize(); err != nil {
		return err
	}
	if s.Type != TypeWordPress {
		s.AutoUpdate = AutoUpdate{}
	}
	return s.AutoUpdate.normalize()
}

// Settings are the panel-wide options edited on the Settings page.
type Settings struct {
	// TrustedProxies are the addresses of Halyard nodes and VIPs. Requests
	// from them may set the client IP (X-Real-IP) and scheme (X-Forwarded-Proto).
	TrustedProxies []string `json:"trusted_proxies"`
	// ProxyOnly refuses site requests that do not come from a trusted proxy.
	ProxyOnly  bool   `json:"proxy_only"`
	DefaultPHP string `json:"default_php"`

	BackupEnabled bool   `json:"backup_enabled"`
	BackupTime    string `json:"backup_time"` // HH:MM local time
	BackupKeep    int    `json:"backup_keep"`
	BackupDir     string `json:"backup_dir"`

	WPLocale     string `json:"wp_locale"`
	WPAdminEmail string `json:"wp_admin_email"`
	WPAdminUser  string `json:"wp_admin_user"`

	// Cloudflare Access sign-in for the panel.
	AccessTeam   string   `json:"access_team"`
	AccessAUD    string   `json:"access_aud"`
	AccessEmails []string `json:"access_emails"`

	Halyard HalyardSettings `json:"halyard"`

	// S3 copies every backup to S3-compatible storage (Backblaze B2, R2...).
	S3 S3Settings `json:"s3"`

	// CloudflareToken may purge Cloudflare's cache (Zone > Cache Purge).
	CloudflareToken string `json:"cloudflare_token,omitempty"`
	// ACME is Let's Encrypt for sites Berth serves over HTTPS itself.
	ACME ACMESettings `json:"acme"`
	// Brand is what hosting customers see.
	Brand BrandSettings `json:"brand"`
	// NoUptime turns off uptime checks for every site.
	NoUptime bool `json:"no_uptime"`
	// NoVulnScan turns off the daily WordPress vulnerability check.
	NoVulnScan bool `json:"no_vuln_scan"`
}

// S3Settings is an off-site copy of backups.
type S3Settings struct {
	Enabled   bool   `json:"enabled"`
	Endpoint  string `json:"endpoint"` // host, e.g. s3.eu-central-003.backblazeb2.com
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"` // folder inside the bucket
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key,omitempty"`
	PathStyle bool   `json:"path_style"` // for MinIO and similar
	Keep      int    `json:"keep"`       // backups kept per site off-site
	// LocalKeep overrides BackupKeep while S3 is on (0 = same as BackupKeep).
	LocalKeep int `json:"local_keep"`
}

// BrandSettings is the face of the panel for hosting customers.
type BrandSettings struct {
	Name         string `json:"name"` // replaces "Berth" in the panel ("" keeps it)
	SupportEmail string `json:"support_email"`
	SupportURL   string `json:"support_url"`
	// IPv4 and IPv6 are this server's public addresses, which customers point
	// their domains at.
	IPv4 string `json:"ipv4"`
	IPv6 string `json:"ipv6"`
	// Logo and Favicon are the uploaded files' names under the data
	// directory's brand folder ("" = Berth's own). The panel sets them.
	Logo    string `json:"logo"`
	Favicon string `json:"favicon"`
}

func (b *BrandSettings) normalize() error {
	b.Name, b.SupportEmail, b.SupportURL = strings.TrimSpace(b.Name), strings.TrimSpace(b.SupportEmail), strings.TrimSpace(b.SupportURL)
	b.IPv4, b.IPv6 = strings.TrimSpace(b.IPv4), strings.TrimSpace(b.IPv6)
	if len(b.Name) > 40 {
		return errors.New("the brand name can be at most 40 characters")
	}
	if b.SupportEmail != "" && !strings.Contains(b.SupportEmail, "@") {
		return errors.New("the support email looks invalid")
	}
	if b.SupportURL != "" && !strings.HasPrefix(b.SupportURL, "https://") && !strings.HasPrefix(b.SupportURL, "http://") {
		return errors.New("the support link must start with https://")
	}
	if ip := net.ParseIP(b.IPv4); b.IPv4 != "" && (ip == nil || ip.To4() == nil) {
		return fmt.Errorf("%q is not an IPv4 address", b.IPv4)
	}
	if ip := net.ParseIP(b.IPv6); b.IPv6 != "" && (ip == nil || ip.To4() != nil) {
		return fmt.Errorf("%q is not an IPv6 address", b.IPv6)
	}
	return nil
}

// ACMESettings configures Let's Encrypt.
type ACMESettings struct {
	// Email gets Let's Encrypt's expiry warnings ("" uses the first admin's).
	Email string `json:"email"`
	// Staging uses Let's Encrypt's staging server (untrusted certificates,
	// generous rate limits) for testing.
	Staging bool `json:"staging"`
	// Off stops Berth from requesting certificates.
	Off bool `json:"off"`
}

// HalyardSettings connects Berth to a Halyard load balancer.
type HalyardSettings struct {
	URL      string `json:"url"` // e.g. https://10.0.30.11:8443
	Token    string `json:"token,omitempty"`
	PoolID   string `json:"pool_id"`   // the pool that points at this Berth server
	VIPID    string `json:"vip_id"`    // default VIP for new sites
	Exposure string `json:"exposure"`  // default exposure for new sites
	Insecure bool   `json:"insecure"`  // skip TLS verification (self-signed UI cert)
	AutoCert bool   `json:"auto_cert"` // request a certificate for each site
	// NoDNS stops new sites from managing Cloudflare DNS by default.
	NoDNS bool `json:"no_dns"`
}

var timeRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Normalize fills defaults and validates settings.
func (st *Settings) Normalize() error {
	if err := st.Brand.normalize(); err != nil {
		return err
	}
	var tp []string
	for _, p := range st.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if net.ParseIP(p) == nil {
			if _, _, err := net.ParseCIDR(p); err != nil {
				return fmt.Errorf("trusted proxy %q is not an IP address or CIDR", p)
			}
		}
		tp = append(tp, p)
	}
	st.TrustedProxies = tp
	if st.ProxyOnly && len(tp) == 0 {
		return fmt.Errorf("add at least one trusted proxy before refusing other traffic")
	}
	if st.DefaultPHP == "" {
		st.DefaultPHP = "8.4"
	}
	if !SupportedPHP(st.DefaultPHP) {
		return fmt.Errorf("PHP %q is not supported", st.DefaultPHP)
	}
	if st.BackupTime == "" {
		st.BackupTime = "02:30"
	}
	if !timeRe.MatchString(st.BackupTime) {
		return fmt.Errorf("backup time must look like 02:30")
	}
	if st.BackupKeep <= 0 {
		st.BackupKeep = 7
	}
	if st.BackupKeep > 365 {
		return fmt.Errorf("keep at most 365 backups per site")
	}
	if st.BackupDir == "" {
		st.BackupDir = "/var/backups/berth"
	}
	if !strings.HasPrefix(st.BackupDir, "/") {
		return fmt.Errorf("backup folder must be an absolute path")
	}
	if st.WPLocale == "" {
		st.WPLocale = "en_US"
	}
	if !localeRe.MatchString(st.WPLocale) {
		return fmt.Errorf("language %q should look like en_US", st.WPLocale)
	}
	if st.WPAdminUser == "" {
		st.WPAdminUser = "admin"
	}
	for i, e := range st.AccessEmails {
		st.AccessEmails[i] = strings.ToLower(strings.TrimSpace(e))
	}
	if err := st.S3.normalize(); err != nil {
		return err
	}
	// Sites default to proxied Cloudflare DNS. Settings saved before
	// Halyard was connected carried the old LAN default, so move them on.
	if st.Halyard.URL == "" && st.Halyard.Exposure == "lan" {
		st.Halyard.Exposure = ""
	}
	switch st.Halyard.Exposure {
	case "":
		st.Halyard.Exposure = "public"
	case "lan", "public", "tunnel":
	default:
		return fmt.Errorf("unknown exposure %q", st.Halyard.Exposure)
	}
	st.Halyard.URL = strings.TrimRight(strings.TrimSpace(st.Halyard.URL), "/")
	if st.Halyard.URL != "" && !strings.HasPrefix(st.Halyard.URL, "https://") && !strings.HasPrefix(st.Halyard.URL, "http://") {
		return fmt.Errorf("Halyard URL must start with https://")
	}
	return nil
}

// User roles. Admins run the server and see every account; customers see
// only their own account's sites.
const (
	RoleAdmin    = "admin"
	RoleCustomer = "customer"
)

// User is a panel login. Every user belongs to one account.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	TOTPSecret   string `json:"-"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	Role         string `json:"role"`
	AccountID    string `json:"account_id"`
}

// IsAdmin reports whether the user runs the server.
func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

// Account owns sites. A hosting customer is one account; the server's owner
// has an account of their own for their sites.
type Account struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Notes string `json:"notes"`
	// Suspended accounts cannot sign in.
	Suspended bool `json:"suspended"`
	// PlanID is the hosting plan that limits the account ("" = no limits).
	PlanID string `json:"plan_id"`
	// AddonPlans are extra plans on top of PlanID, e.g. business email
	// bought alongside hosting. Their mail and DNS limits add to the plan's.
	AddonPlans []string `json:"addon_plans,omitempty"`
	// ExternalID is the billing system's ID for the account (service ID,
	// client ID...), set through the provisioning API. Unique when set.
	ExternalID string    `json:"external_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Plan is a hosting package: what an account may use. Zero means no limit.
type Plan struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Per account.
	MaxSites int `json:"max_sites"`
	DiskMB   int `json:"disk_mb"` // files and databases of every site
	DNSZones int `json:"dns_zones"`
	// Per site.
	MaxDomains    int `json:"max_domains"` // main domain plus other domains
	MemoryMB      int `json:"memory_mb"`   // PHP memory_limit
	Workers       int `json:"workers"`     // PHP-FPM max_children
	UploadMB      int `json:"upload_mb"`
	DBConnections int `json:"db_connections"` // MariaDB connections per site
	// CPU, memory and process caps for all of the account's PHP together.
	// Any of them gives the account PHP-FPM processes of its own in a
	// systemd slice.
	CPUPercent   int `json:"cpu_percent"` // 100 = one core
	RAMMB        int `json:"ram_mb"`
	MaxProcesses int `json:"max_processes"`
	// AllowShell leaves exec(), system() and friends on in PHP.
	AllowShell bool `json:"allow_shell"`
	NoSFTP     bool `json:"no_sftp"`
	// NoSites is for mail-only plans: the account cannot create websites.
	NoSites bool `json:"no_sites"`
	// Mail (the mailcow plugin), per account. MailDomains 0 = no mail.
	MailDomains   int `json:"mail_domains"`
	Mailboxes     int `json:"mailboxes"`       // across all of its mail domains
	MailAliases   int `json:"mail_aliases"`    // across all of its mail domains
	MailStorageMB int `json:"mail_storage_mb"` // all mailboxes together
}

// Isolated reports whether the plan's accounts get PHP processes of their own.
func (p Plan) Isolated() bool { return p.CPUPercent > 0 || p.RAMMB > 0 || p.MaxProcesses > 0 }

// ShellFunctions are switched off in PHP for plans without AllowShell.
const ShellFunctions = "exec,passthru,shell_exec,system,proc_open,popen,proc_nice,pcntl_exec,pcntl_fork,dl,posix_kill,posix_setuid,posix_setgid"

// Normalize tidies and validates a plan.
func (p *Plan) Normalize() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 60 {
		return errors.New("plan name must be 1-60 characters")
	}
	for _, n := range []int{p.MaxSites, p.DiskMB, p.DNSZones, p.MaxDomains, p.MemoryMB, p.Workers, p.UploadMB, p.DBConnections, p.CPUPercent, p.RAMMB, p.MaxProcesses, p.MailDomains, p.Mailboxes, p.MailAliases, p.MailStorageMB} {
		if n < 0 {
			return errors.New("limits cannot be negative")
		}
	}
	if p.MemoryMB != 0 && p.MemoryMB < 32 {
		return errors.New("PHP memory must be at least 32 MB")
	}
	if p.RAMMB != 0 && p.RAMMB < 64 || p.MaxProcesses != 0 && p.MaxProcesses < 10 {
		return errors.New("an account needs at least 64 MB of memory and 10 processes")
	}
	if p.Workers > 200 || p.UploadMB > 10240 {
		return errors.New("a limit is out of range")
	}
	return nil
}

// Limit returns n capped by a plan limit (0 = none).
func Limit(n, limit int) int {
	if limit > 0 && (n == 0 || n > limit) {
		return limit
	}
	return n
}

// Normalize tidies and validates an account.
func (a *Account) Normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	a.Email = strings.ToLower(strings.TrimSpace(a.Email))
	a.ExternalID = strings.TrimSpace(a.ExternalID)
	if len(a.ExternalID) > 100 {
		return errors.New("external ID can be at most 100 characters")
	}
	if a.Name == "" || len(a.Name) > 100 {
		return errors.New("account name must be 1-100 characters")
	}
	if a.Email != "" && !strings.Contains(a.Email, "@") {
		return errors.New("email looks invalid")
	}
	return nil
}

// WithAddon adds an add-on's mail and DNS limits to a plan. The base plan
// keeps its hosting limits; a base plan without mail or DNS takes the
// add-on's (0 means none there, not unlimited).
func (p Plan) WithAddon(a Plan) Plan {
	sum := func(base, more int) int {
		// 0 = unlimited for these, so unlimited stays unlimited.
		if base == 0 || more == 0 {
			return 0
		}
		return base + more
	}
	hadMail := p.MailDomains > 0
	p.MailDomains += a.MailDomains
	if hadMail {
		p.Mailboxes = sum(p.Mailboxes, a.Mailboxes)
		p.MailAliases = sum(p.MailAliases, a.MailAliases)
		p.MailStorageMB = sum(p.MailStorageMB, a.MailStorageMB)
	} else {
		p.Mailboxes, p.MailAliases, p.MailStorageMB = a.Mailboxes, a.MailAliases, a.MailStorageMB
	}
	p.DNSZones += a.DNSZones
	p.Name += " + " + a.Name
	return p
}

// Event is an entry in the activity log.
type Event struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Kind    string    `json:"kind"`
	Site    string    `json:"site"`
	Message string    `json:"message"`
}

// Job is a long-running task (installing WordPress, backups, restores).
type Job struct {
	ID        int64     `json:"id"`
	Site      string    `json:"site"`
	AccountID string    `json:"-"` // the site's account when the job started
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Status    string    `json:"status"` // running, ok, failed
	Log       string    `json:"log"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	Error     string    `json:"error"`
}

// Backup is a backup archive on disk.
type Backup struct {
	Site   string    `json:"site"`
	File   string    `json:"file"`
	Size   int64     `json:"size"`
	Time   time.Time `json:"time"`
	Local  bool      `json:"local"`  // in the backup folder
	Remote bool      `json:"remote"` // in S3
	// Gone marks backups of a site that has since been deleted.
	Gone bool `json:"gone,omitempty"`
}

func (c *S3Settings) normalize() error {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	// "http://" stays, for MinIO on the LAN; HTTPS is the default.
	c.Endpoint = strings.TrimRight(strings.TrimPrefix(c.Endpoint, "https://"), "/")
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Region = strings.TrimSpace(c.Region)
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	if c.Keep <= 0 {
		c.Keep = 30
	}
	if c.Keep > 3650 || c.LocalKeep < 0 || c.LocalKeep > 365 {
		return fmt.Errorf("S3: retention is out of range")
	}
	if c.Enabled && (c.Endpoint == "" || c.Bucket == "" || c.AccessKey == "") {
		return fmt.Errorf("S3 needs an endpoint, a bucket and an access key")
	}
	if c.Endpoint != "" && strings.ContainsAny(strings.TrimPrefix(c.Endpoint, "http://"), " /") {
		return fmt.Errorf("S3 endpoint should be a host name like s3.eu-central-003.backblazeb2.com")
	}
	return nil
}
