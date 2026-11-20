// Package model holds the objects Berth stores and renders into nginx,
// PHP-FPM and MariaDB.
package model

import (
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
	ID         string   `json:"id"`   // equal to Name
	Name       string   `json:"name"` // short name: Linux user, database and directory
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
	HTTPS       bool        `json:"https"`
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
	CreatedAt   time.Time   `json:"created_at"`
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
	return nil
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

// User is a panel login.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	TOTPSecret   string `json:"-"`
	TOTPEnabled  bool   `json:"totp_enabled"`
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
	ID       int64     `json:"id"`
	Site     string    `json:"site"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Status   string    `json:"status"` // running, ok, failed
	Log      string    `json:"log"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Error    string    `json:"error"`
}

// Backup is a backup archive on disk.
type Backup struct {
	Site   string    `json:"site"`
	File   string    `json:"file"`
	Size   int64     `json:"size"`
	Time   time.Time `json:"time"`
	Local  bool      `json:"local"`  // in the backup folder
	Remote bool      `json:"remote"` // in S3
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
