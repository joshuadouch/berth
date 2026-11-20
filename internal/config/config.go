// Package config loads the node-local Berth configuration file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is /etc/berth/berth.yaml. Sites and settings live in the database;
// this file only describes the machine.
type Config struct {
	// Listen is the management UI/API address.
	Listen string `yaml:"listen"`
	// TLSCert/TLSKey for the management UI. Generated self-signed if empty.
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
	DataDir string `yaml:"data_dir"`
	Name    string `yaml:"name"` // shown in the UI; defaults to the hostname

	// SitesDir holds one directory per site.
	SitesDir string `yaml:"sites_dir"`
	// LogDir holds nginx logs per site.
	LogDir string `yaml:"log_dir"`

	Nginx struct {
		Binary    string `yaml:"binary"`
		ConfDir   string `yaml:"conf_dir"` // Berth owns this directory
		ReloadCmd string `yaml:"reload_cmd"`
		User      string `yaml:"user"` // the user nginx workers run as
		IPv6      bool   `yaml:"ipv6"` // listen on [::]:80 as well
	} `yaml:"nginx"`

	PHP struct {
		// {v} is replaced with the PHP version, e.g. 8.3.
		FPMBinary string `yaml:"fpm_binary"`
		PoolDir   string `yaml:"pool_dir"`
		FPMConfig string `yaml:"fpm_config"`
		ReloadCmd string `yaml:"reload_cmd"`
		CLI       string `yaml:"cli"`
		Socket    string `yaml:"socket"` // {site} is replaced with the site name
		// InstallCmd installs a PHP version and the extensions sites need.
		InstallCmd string `yaml:"install_cmd"`
	} `yaml:"php"`

	MySQL struct {
		Client string `yaml:"client"` // mariadb, run as root over the socket
		Dump   string `yaml:"dump"`
		// Socket is where the database editor connects as each site's user.
		Socket string `yaml:"socket"`
	} `yaml:"mysql"`

	WPCLI string `yaml:"wp_cli"`

	SFTP struct {
		Enabled bool   `yaml:"enabled"`
		Group   string `yaml:"group"`
		KeysDir string `yaml:"keys_dir"`
	} `yaml:"sftp"`

	// DryRun writes config but never runs system commands. For development.
	DryRun bool `yaml:"dry_run"`

	// UpdateRepo is the GitHub repository releases are installed from.
	UpdateRepo string `yaml:"update_repo"`
}

// Default returns a config with sensible defaults for a Debian install.
func Default() *Config {
	c := &Config{Listen: ":8443", DataDir: "/var/lib/berth", UpdateRepo: "joshuadouch/berth",
		SitesDir: "/srv/sites", LogDir: "/var/log/berth"}
	c.Name, _ = os.Hostname()
	c.Nginx.Binary = "/usr/sbin/nginx"
	c.Nginx.ConfDir = "/etc/nginx/berth"
	c.Nginx.ReloadCmd = "systemctl reload nginx"
	c.Nginx.User = "www-data"
	c.Nginx.IPv6 = true
	c.PHP.FPMBinary = "/usr/sbin/php-fpm{v}"
	c.PHP.PoolDir = "/etc/php/{v}/fpm/pool.d"
	c.PHP.FPMConfig = "/etc/php/{v}/fpm/php-fpm.conf"
	c.PHP.ReloadCmd = "systemctl reload-or-restart php{v}-fpm"
	c.PHP.CLI = "/usr/bin/php{v}"
	c.PHP.Socket = "/run/php/berth-{site}.sock"
	c.PHP.InstallCmd = "apt-get update -q && apt-get install -y -q php{v}-fpm php{v}-cli php{v}-mysql php{v}-curl php{v}-gd php{v}-intl php{v}-mbstring php{v}-xml php{v}-zip php{v}-bcmath php{v}-soap php{v}-gmp && (apt-get install -y -q php{v}-imagick php{v}-opcache || true) && systemctl enable --now php{v}-fpm"
	c.MySQL.Client = "/usr/bin/mariadb"
	c.MySQL.Dump = "/usr/bin/mariadb-dump"
	c.MySQL.Socket = "/run/mysqld/mysqld.sock"
	c.WPCLI = "/usr/local/bin/wp"
	c.SFTP.Enabled = true
	c.SFTP.Group = "berth-sftp"
	c.SFTP.KeysDir = "/etc/ssh/berth_keys"
	return c
}

// Load reads path over the defaults. A missing file yields defaults.
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, c.check()
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.check()
}

func (c *Config) check() error {
	if c.SitesDir == "" || !filepath.IsAbs(c.SitesDir) {
		return fmt.Errorf("sites_dir must be an absolute path")
	}
	if c.Name == "" {
		c.Name = "berth"
	}
	return nil
}

// Path joins a path under the data directory.
func (c *Config) Path(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir}, parts...)...)
}

// V replaces {v} in a PHP path template.
func V(tmpl, version string) string { return strings.ReplaceAll(tmpl, "{v}", version) }

// Socket returns the PHP-FPM socket for a site.
func (c *Config) Socket(site string) string { return strings.ReplaceAll(c.PHP.Socket, "{site}", site) }
