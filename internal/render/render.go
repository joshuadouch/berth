// Package render turns sites and settings into nginx and PHP-FPM config.
package render

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/joshuadouch/berth/internal/model"
)

// Paths are where rendered files go and what they refer to.
type Paths struct {
	SitesDir   string // /srv/sites
	LogDir     string // /var/log/berth
	NginxConf  string // /etc/nginx/berth
	PoolDir    string // /etc/php/{v}/fpm/pool.d
	SocketTmpl string // /run/php/berth-{site}.sock
	IPv6       bool   // also listen on [::]:80
}

// Output is the full set of managed files.
type Output struct {
	// Files maps absolute paths to their content.
	Files map[string]string
	// PHPVersions are the versions that have at least one pool.
	PHPVersions []string
}

// SiteDir is a site's home directory.
func (p Paths) SiteDir(name string) string { return filepath.Join(p.SitesDir, name) }

// Socket is a site's PHP-FPM socket.
func (p Paths) Socket(name string) string { return strings.ReplaceAll(p.SocketTmpl, "{site}", name) }

// PoolFile is where a site's pool lives for a PHP version.
func (p Paths) PoolFile(version, name string) string {
	return filepath.Join(strings.ReplaceAll(p.PoolDir, "{v}", version), "berth-"+name+".conf")
}

// Render renders every site. Sites must already be validated.
func Render(p Paths, st model.Settings, sites []model.Site) (Output, error) {
	out := Output{Files: map[string]string{}}
	out.Files[filepath.Join(p.NginxConf, "http.conf")] = httpConf(p, st)
	out.Files[filepath.Join(p.NginxConf, "fastcgi.conf")] = fastcgiConf
	versions := map[string]bool{}
	names := map[string]string{}
	for _, s := range sites {
		for _, h := range s.Hostnames() {
			if other, ok := names[h]; ok {
				return out, fmt.Errorf("%s is used by both %s and %s", h, other, s.Name)
			}
			names[h] = s.Name
		}
		out.Files[filepath.Join(p.NginxConf, "sites", s.Name+".conf")] = Vhost(p, st, s)
		out.Files[p.PoolFile(s.PHPVersion, s.Name)] = Pool(p, s)
		versions[s.PHPVersion] = true
	}
	for v := range versions {
		out.PHPVersions = append(out.PHPVersions, v)
	}
	sort.Strings(out.PHPVersions)
	return out, nil
}

const header = "# Managed by Berth. Changes made here are overwritten.\n"

func httpConf(p Paths, st model.Settings) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString(header)
	w("log_format berth '$remote_addr - $remote_user [$time_local] \"$request\" $status $body_bytes_sent \"$http_referer\" \"$http_user_agent\" $request_time';")
	w("")
	w("# Trusted proxies (Halyard) may set the client address and scheme.")
	w("geo $realip_remote_addr $berth_from_proxy {")
	w("    default 0;")
	w("    127.0.0.1 1;")
	w("    ::1 1;")
	for _, t := range st.TrustedProxies {
		w("    %s 1;", t)
	}
	w("}")
	w("set_real_ip_from 127.0.0.1;")
	w("set_real_ip_from ::1;")
	for _, t := range st.TrustedProxies {
		w("set_real_ip_from %s;", t)
	}
	w("real_ip_header X-Real-IP;")
	w("map \"$berth_from_proxy:$http_x_forwarded_proto\" $berth_https {")
	w("    \"1:https\" on;")
	w("    default $https;")
	w("}")
	w("map $berth_https $berth_scheme {")
	w("    on https;")
	w("    default http;")
	w("}")
	w("map $berth_https $berth_port {")
	w("    on 443;")
	w("    default $server_port;")
	w("}")
	w("")
	w("# Requests for names no site serves. /berth-health is for load balancer checks.")
	w("server {")
	w("    listen 80 default_server;")
	if p.IPv6 {
		w("    listen [::]:80 default_server;")
	}
	w("    server_name _;")
	w("    access_log off;")
	w("    location = /berth-health {")
	w("        default_type text/plain;")
	w("        return 200 \"ok\\n\";")
	w("    }")
	w("    location / {")
	w("        return 444;")
	w("    }")
	w("}")
	return b.String()
}

// fastcgiConf replaces Debian's fastcgi_params so HTTPS, the scheme and the
// port reflect what the visitor used at the proxy.
const fastcgiConf = header + `fastcgi_param  QUERY_STRING       $query_string;
fastcgi_param  REQUEST_METHOD     $request_method;
fastcgi_param  CONTENT_TYPE       $content_type;
fastcgi_param  CONTENT_LENGTH     $content_length;
fastcgi_param  SCRIPT_NAME        $fastcgi_script_name;
fastcgi_param  SCRIPT_FILENAME    $document_root$fastcgi_script_name;
fastcgi_param  REQUEST_URI        $request_uri;
fastcgi_param  DOCUMENT_URI       $document_uri;
fastcgi_param  DOCUMENT_ROOT      $document_root;
fastcgi_param  SERVER_PROTOCOL    $server_protocol;
fastcgi_param  REQUEST_SCHEME     $berth_scheme;
fastcgi_param  HTTPS              $berth_https if_not_empty;
fastcgi_param  GATEWAY_INTERFACE  CGI/1.1;
fastcgi_param  SERVER_SOFTWARE    nginx;
fastcgi_param  REMOTE_ADDR        $remote_addr;
fastcgi_param  REMOTE_PORT        $remote_port;
fastcgi_param  REMOTE_USER        $remote_user;
fastcgi_param  SERVER_ADDR        $server_addr;
fastcgi_param  SERVER_PORT        $berth_port;
fastcgi_param  SERVER_NAME        $host;
fastcgi_param  REDIRECT_STATUS    200;
fastcgi_param  HTTP_PROXY         "";
`

// DocRoot is the absolute document root of a site.
func DocRoot(p Paths, s model.Site) string {
	r := filepath.Join(p.SiteDir(s.Name), "public")
	if s.DocRoot != "" {
		r = filepath.Join(r, s.DocRoot)
	}
	return r
}

// Vhost renders a site's nginx server block.
func Vhost(p Paths, st model.Settings, s model.Site) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString(header)
	w("# %s (%s)", s.Domain, s.Type.Label())
	names := s.Hostnames()
	if s.RedirectAliases && len(s.Aliases) > 0 {
		names = []string{s.Domain}
		w("server {")
		w("    listen 80;")
		if p.IPv6 {
			w("    listen [::]:80;")
		}
		w("    server_name %s;", strings.Join(s.Aliases, " "))
		w("    access_log %s berth;", filepath.Join(p.LogDir, s.Name, "access.log"))
		w("    return 301 $berth_scheme://%s$request_uri;", s.Domain)
		w("}")
	}
	w("server {")
	w("    listen 80;")
	if p.IPv6 {
		w("    listen [::]:80;")
	}
	w("    server_name %s;", strings.Join(names, " "))
	w("    root %s;", DocRoot(p, s))
	w("    index index.php index.html index.htm;")
	w("    access_log %s berth;", filepath.Join(p.LogDir, s.Name, "access.log"))
	w("    error_log %s warn;", filepath.Join(p.LogDir, s.Name, "error.log"))
	w("    client_max_body_size %dm;", s.UploadMB)
	w("    server_tokens off;")
	if st.ProxyOnly {
		w("    if ($berth_from_proxy = 0) {")
		w("        return 403;")
		w("    }")
	}
	if !s.Enabled {
		w("    location / {")
		w("        default_type text/html;")
		w("        return 503 '<!doctype html><title>Unavailable</title><p style=\"font:16px system-ui;margin:20vh auto;max-width:30em;text-align:center\">This site is temporarily unavailable.</p>';")
		w("    }")
		w("}")
		return b.String()
	}
	if c := strings.TrimSpace(s.NginxCustom); c != "" {
		w("")
		w("    # Custom directives, edited on the site's Settings tab. They come")
		w("    # first, so their regex locations are checked before Berth's.")
		for _, l := range strings.Split(strings.ReplaceAll(c, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(l) == "" {
				w("")
			} else {
				w("    %s", strings.TrimRight(l, " \t"))
			}
		}
		w("    # End of custom directives.")
	}
	w("")
	w("    location ~ /\\.(?!well-known) {")
	w("        deny all;")
	w("    }")
	w("    location = /favicon.ico {")
	w("        log_not_found off;")
	w("        access_log off;")
	w("    }")
	w("    location = /robots.txt {")
	w("        log_not_found off;")
	w("        access_log off;")
	if s.Type == model.TypeWordPress {
		w("        try_files $uri /index.php?$args;")
	}
	w("    }")
	if s.Type == model.TypeWordPress {
		w("    # Uploaded files are never run as PHP.")
		w("    location ~* ^/wp-content/uploads/.*\\.(?:php[0-9]?|phtml|phar)$ {")
		w("        deny all;")
		w("    }")
	}
	w("    location ~* \\.(?:css|js|mjs|jpe?g|png|gif|webp|avif|svg|ico|woff2?|ttf|otf|eot|mp4|webm|mp3|pdf)$ {")
	w("        expires 30d;")
	if s.FrontController || s.Type == model.TypeWordPress {
		w("        try_files $uri /index.php?$args;")
	} else {
		w("        try_files $uri =404;")
	}
	w("    }")
	w("    location / {")
	if s.FrontController || s.Type == model.TypeWordPress {
		w("        try_files $uri $uri/ /index.php?$args;")
	} else {
		w("        try_files $uri $uri/ =404;")
	}
	w("    }")
	w("    location ~ [^/]\\.php(/|$) {")
	w("        fastcgi_split_path_info ^(.+?\\.php)(/.*)$;")
	w("        try_files $fastcgi_script_name =404;")
	w("        include %s;", filepath.Join(p.NginxConf, "fastcgi.conf"))
	w("        fastcgi_param PATH_INFO $fastcgi_path_info;")
	w("        fastcgi_pass unix:%s;", p.Socket(s.Name))
	w("        fastcgi_read_timeout 300s;")
	w("        fastcgi_buffers 16 16k;")
	w("        fastcgi_buffer_size 32k;")
	w("    }")
	w("}")
	return b.String()
}

// Pool renders a site's PHP-FPM pool.
func Pool(p Paths, s model.Site) string {
	dir := p.SiteDir(s.Name)
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString("; Managed by Berth. Changes made here are overwritten.\n")
	w("[berth-%s]", s.Name)
	w("user = %s", s.User())
	w("group = %s", s.User())
	w("listen = %s", p.Socket(s.Name))
	w("listen.owner = www-data")
	w("listen.group = www-data")
	w("listen.mode = 0660")
	w("pm = ondemand")
	w("pm.max_children = %d", s.MaxChildren)
	w("pm.process_idle_timeout = 30s")
	w("pm.max_requests = 1000")
	w("chdir = /")
	w("catch_workers_output = yes")
	w("decorate_workers_output = no")
	w("security.limit_extensions = .php")
	w("env[TMPDIR] = %s/tmp", dir)
	w("env[HOME] = %s/tmp", dir)
	w("php_admin_value[open_basedir] = %s/:/usr/share/php/:/dev/urandom", dir)
	w("php_admin_value[upload_tmp_dir] = %s/tmp", dir)
	w("php_admin_value[sys_temp_dir] = %s/tmp", dir)
	w("php_admin_value[session.save_path] = %s/sessions", dir)
	w("php_admin_value[error_log] = %s/logs/php-error.log", dir)
	w("php_admin_flag[log_errors] = on")
	w("php_admin_flag[display_errors] = off")
	w("php_admin_flag[expose_php] = off")
	w("php_admin_value[memory_limit] = %dM", s.MemoryMB)
	w("php_admin_value[upload_max_filesize] = %dM", s.UploadMB)
	w("php_admin_value[post_max_size] = %dM", s.UploadMB)
	ps := s.PHP.WithDefaults()
	w("php_admin_value[max_execution_time] = %d", ps.MaxExecution)
	w("php_admin_value[max_input_vars] = %d", ps.MaxInputVars)
	w("php_admin_value[max_input_time] = %d", ps.MaxInputTime)
	w("; Requests slower than 5 seconds log a PHP stack trace (Performance tab).")
	w("request_slowlog_timeout = 5s")
	w("slowlog = %s/logs/php-slow.log", dir)
	if lines, err := model.ParsePHPCustom(ps.Custom); err == nil && len(lines) > 0 {
		w("; Custom settings from the site's Settings tab.")
		for _, l := range lines {
			switch strings.ToLower(l.Value) {
			case "on", "off", "true", "false", "yes", "no":
				w("php_admin_flag[%s] = %s", l.Key, strings.ToLower(l.Value))
			default:
				w("php_admin_value[%s] = %s", l.Key, iniValue(l.Value))
			}
		}
	}
	return b.String()
}

var plainINI = regexp.MustCompile(`^[A-Za-z0-9_./:,-]*$`)

// iniValue quotes a php.ini value unless it is plain.
func iniValue(v string) string {
	if plainINI.MatchString(v) {
		return v
	}
	return `"` + v + `"`
}
