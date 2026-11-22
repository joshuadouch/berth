# Berth

A small hosting control panel for your own WordPress and PHP sites, built to
sit behind [Halyard](https://github.com/joshuadouch/halyard). Berth is a single
Go binary with a web UI. It drives **nginx**, **PHP-FPM** (several versions side
by side, from packages.sury.org), **MariaDB** and **WP-CLI** on one Debian
machine.

Berth also does shared hosting. Every site belongs to an **account**. The
first login is an admin who runs the server; admins add customer accounts on
the Accounts page, each with its own logins. Customers only see their own
sites, backups and activity, and never the server's settings, plugins, Halyard
or Cloudflare. Admins see every account and can move a site between them.
Panels from before accounts existed are moved to one account owned by the
existing admin on the first start.

**Plans** (Platform → Plans) are hosting packages. An account on a plan is held
to its limits: number of sites, disk space for files and databases (over it,
no new sites or uploads), DNS domains, domains per site, and per-site PHP
memory, workers, upload size and MariaDB connections. Plans block PHP's shell
functions (`exec`, `system`, `proc_open` and the like) unless they allow them,
and can turn SFTP off. Changing a plan, or an account's plan, applies to its
existing sites at once. Accounts without a plan have no limits.

A plan can also cap **CPU, memory and processes** for all of an account's PHP
together. Its accounts then run PHP in PHP-FPM masters of their own
(`berth-fpm@<account>-<version>.service`, config in `/etc/berth/fpm`) inside a
systemd slice (`berth-acct-<account>.slice`) that holds the limits, so one busy
customer cannot starve the others. The socket names stay the same, so nginx is
untouched. In an LXC the limits need the cgroup controllers Proxmox delegates
by default (cgroup v2); check with `systemctl status berth-acct-<account>.slice`.

- **One-click WordPress.** Enter a domain and Berth creates the site, its
  database, installs WordPress, sets pretty permalinks and shows the admin
  password once.
- **Move sites in.** When creating a site, upload its files (`.zip`, `.tar`,
  `.tar.gz`, including cPanel or Plesk account backups) and its database dump
  (`.sql` or `.sql.gz`). Berth finds the web root inside the archive, imports
  the dump as the site's own database user (DEFINER clauses and MySQL 8
  collations are fixed on the way), and for WordPress points `wp-config.php` at
  the new database and rewrites the old address to the new domain.
- **PHP + database** and **PHP** sites for everything else, with a document root
  in a subfolder (Laravel, Bedrock) and pretty URLs through `index.php` when you
  want them.
- **Isolation.** Each site has its own Linux user, PHP-FPM pool (with
  `open_basedir`), database and database user. One site cannot read another's
  files.
- **Save means apply.** Every change renders nginx and PHP-FPM config, checks it
  with `nginx -t` and `php-fpm -t`, and reloads. A config that fails a check is
  rolled back and nothing changes.
- **Backups.** Nightly per-site backups (files, database dump and settings in
  one `.tar.gz`), retention per site, download, and restore (a safety backup is
  taken first). Deleting a site keeps a final backup by default.
- **WordPress tools.** Version and update status, one-click update of core,
  plugins, themes and translations (backed up first), password reset for any
  WordPress user, and WP-Cron run by Berth every five minutes instead of on page
  views. Changing a WordPress site's domain rewrites its URLs in the database.
- **File manager.** Browse, edit, upload (files or whole folders, by drag and
  drop), download, rename, delete, change permissions and unzip, inside the
  site's folder only.
- **Database editor.** Browse and search tables, edit, add and delete rows, and
  run SQL, connected as the site's own database user.
- **Off-site backups** to any S3-compatible storage (Backblaze B2, Cloudflare
  R2, Wasabi, AWS, MinIO) with their own retention.
- **One-click restore** of everything, only the files or only the database, and
  a browser for the files inside any backup: download one file or put back
  chosen files and folders.
- **PHP versions per site**, installed from the System page, with PHP security
  updates checked daily and installed in one click.
- **SFTP per site**, locked into the site's folder, with a password or SSH keys.
- **Logs.** Access, nginx error and PHP error logs for each site in the UI.
- **Plugins.** Extra features you switch on from the Plugins page. The first is
  **Mailcow**: mail domains, mailboxes and aliases, with outgoing mail sent
  through Amazon SES (see below).
- **Analytics.** Requests, visitors, bandwidth, top pages, referrers, browsers,
  404s and server errors per site, counted from the nginx access log.
- **Performance.** Average and slowest requests, the slowest pages, PHP stack
  traces of requests that run for 5 seconds or more (naming the plugin or theme
  responsible) and the site's slow database queries.
- **Resources.** CPU and memory of each site's processes, and disk and database
  size, over 30 days.
- **Uptime checks.** Every live site is requested once a minute through its
  public address; outages are logged in Activity, with uptime history and
  response times on the site's Analytics tab.
- **Security.** A daily check of WordPress, plugins and themes against the free
  [WPVulnerability](https://www.wpvulnerability.net) database.
- **Safe updates.** WordPress updates (now or on a schedule per site) back up
  first, check the home and login pages afterwards and roll back if they broke.
- **Search and replace** in a WordPress database, with a preview, through WP-CLI.
- **Clear Cloudflare's cache** for a site's domains in one click.
- PHP version, memory, upload limit, workers, execution time, input vars and any
  other `php.ini` setting per site; extra domains with an
  optional redirect to the main one; take a site offline with a 503 page.

## Install on Proxmox VE

Run this in the Proxmox node shell. It creates an unprivileged Debian 12 LXC and
installs everything inside it.

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/berth/main/ct/berth.sh)"
```

The script asks for the container ID, size, bridge and address (a fixed address
is best, since Halyard points at it), the first PHP version, and optionally a
folder on the Proxmox host to keep backups in. That folder is bind-mounted at
`/var/backups/berth`; an unprivileged container writes to it as uid 100000, so
give that user write access. Every prompt can be preset with an environment
variable (see the top of `ct/berth.sh`), and `UNATTENDED=1` accepts defaults.

Open `https://<container-ip>:8443` and create the admin account straight away:
until it exists, anyone who can reach the port can claim it.

### Any Debian 12 or 13 VM or LXC

```bash
curl -fsSLO https://raw.githubusercontent.com/joshuadouch/berth/main/install/berth-install.sh
bash berth-install.sh
```

Options are environment variables: `BERTH_VERSION`, `BERTH_PHP` (default
`8.4`), `BERTH_LISTEN`, `BERTH_BINARY` (use a binary you already copied) and
`BERTH_NO_SFTP=1`.

## HTTPS with Let's Encrypt

A site set to **HTTPS (free Let's Encrypt certificate)** gets its certificate
from Let's Encrypt as soon as it is live, and Berth renews it 30 days before it
expires. Until the first one arrives the site answers on plain HTTP. Customers'
HTTPS sites always work this way. For it to work:

- every domain of the site points at this server, and
- ports 80 and 443 reach it (the HTTP-01 challenge is served from
  `/var/www/berth-acme` on port 80).

A failed request is retried after an hour, then backs off to once a day; the
error is on the site's Overview, where **Request now** tries again at once.
Settings has the contact email, Let's Encrypt's staging server for testing,
and a switch to stop requests. Certificates live in `/var/lib/berth/certs`.

Sites published on Halyard (and admin sites set to **HTTPS (Halyard or
Cloudflare in front ends it)**) keep the setup below: plain HTTP on port 80,
with the proxy ending TLS.

## Behind Halyard

Sites listen on plain HTTP port 80. Halyard terminates TLS and owns the
certificates.

1. In Halyard, add a pool with this server's address on port 80. Use an HTTP
   health check on `/berth-health`.
2. For each site, add a Halyard host with its domains pointing at that pool.
3. In Berth, open **Settings** and add your Halyard nodes (or their subnet) as
   **trusted proxies**.

Trusted proxies may tell sites two things, and nobody else can:

- **The visitor's address.** Halyard sends `X-Real-IP` (already restored from
  `CF-Connecting-IP` for Cloudflare traffic), and nginx uses it as the client
  address, so PHP, WordPress and the access logs see the real visitor.
- **HTTPS.** When Halyard sends `X-Forwarded-Proto: https`, nginx gives PHP
  `HTTPS=on`, port 443 and the `https` scheme. WordPress builds `https://`
  links and never loops on redirects, with no plugin or `wp-config.php` change.

### Publishing sites on Halyard automatically

Berth can create each site's Halyard host for you.

1. In Halyard, open **Settings → API tokens** and create a token.
2. In Berth, open **Settings → Halyard**, enter Halyard's URL (for example
   `https://10.0.30.11:8443`, with **Accept Halyard's self-signed certificate**
   on) and the token, and save.
3. Choose the pool for this server, or press **Create pool** to add one that
   points at this machine on port 80 with a `/berth-health` check. Pick a
   default VIP and exposure, and whether HTTPS sites get a certificate.

New sites then have a **Publish on Halyard** option, and existing sites have a
Halyard card on their Overview tab. Berth keeps the host's domains, VIP,
exposure and HTTPS in step with the site, and leaves anything else you change on
Halyard (HSTS, allow lists, extra routes) alone. DNS is managed by default:
public and tunnel sites get proxied (orange cloud) Cloudflare records, and LAN
sites get a plain record pointing at the VIP. Halyard needs its Cloudflare API
token (and, for public sites, your public IP) set on its Cloudflare page. When an HTTPS site's domains are
not covered by an existing certificate (a wildcard, say), Berth asks Halyard to
issue one. Deleting a site, or pressing **Remove from Halyard**, deletes its host
and the certificate Berth requested once nothing else uses it.

Moving a site from another server that Halyard already fronts? If a Halyard
host serves exactly the site's domains (the main one and its other domains, in
any order), Berth takes that host over: it points the default route at Berth's
pool and keeps the host's VIP, exposure, DNS, certificate and other routes, so
the switch is just the backend. If a host shares only some of the domains, or
serves others too, publishing stops with an error naming them, so another site
is never moved by accident.

### Off-site backups

In **Settings → Off-site backups (S3)**, enter the endpoint, region, bucket and
an application key. For Backblaze B2, create a private bucket, then an
application key limited to that bucket, and use the S3 endpoint shown on the
bucket page (such as `s3.eu-central-003.backblazeb2.com`, region
`eu-central-003`). **Test connection** checks that Berth can write and delete.
Every backup is then copied to `<folder>/<site>/` in the bucket. Restoring or
browsing a backup that is only off-site downloads it first.

Turn on **Refuse visitors that skip the load balancer** to answer 403 to any
request that does not come from a trusted proxy.

## Monitoring and analytics

- Analytics read `/var/log/berth/<site>/access.log` every five minutes. Requests
  Cloudflare serves from its cache never reach the server and are not counted.
- PHP-FPM writes a stack trace to `/srv/sites/<site>/logs/php-slow.log` for any
  request still running after 5 seconds.
- Berth turns on MariaDB's slow query log (`/var/log/mysql/mariadb-slow.log`,
  queries of a second or more) and checks every hour that it is still on.
- Uptime checks use each site's public address, so they test DNS, Halyard and
  Cloudflare too. When a check fails but the site answers locally, the outage
  says so. Turn checks off per site on its Settings tab, or for every site in
  **Settings → Monitoring and Cloudflare**.
- History is kept for 30 days (uptime and hourly traffic for 90).
- **Clear cache** needs a Cloudflare API token with **Zone → Zone → Read** and
  **Zone → Cache Purge → Purge**, saved in **Settings → Monitoring and Cloudflare**.

## Sites on disk

```
/srv/sites/<name>/            root, 0751 (SFTP chroot)
/srv/sites/<name>/public/     the web root, owned by site_<name>
/srv/sites/<name>/logs/       PHP error log
/srv/sites/<name>/tmp/        uploads and temp files, private
/srv/sites/<name>/sessions/   PHP sessions, private
/var/log/berth/<name>/        nginx access and error logs
/var/backups/berth/<name>/    backups
```

Moving a site from another host? Turn on **Move an existing site here** on the
New site page instead of copying files by hand. Uploads wait in
`/var/lib/berth/imports/` and are deleted once imported (or after two days if
unused). Behind Cloudflare, an upload through the proxy is limited to 100 MB;
use Berth's LAN address for bigger sites.

Need something Berth's settings don't cover, such as a redirect, a header or an
extra `location`? Add it under **nginx config** on the site's Settings tab. Your
directives go inside the site's server block, before Berth's own locations, and
are kept whenever Berth rewrites the config. `nginx -t` checks them first; if it
fails, nothing changes and you see nginx's error.

Copied files in as root? Press **Fix permissions** on the site page. To bring in
a backup from elsewhere, copy it into `/var/backups/berth/<name>/` and restore it
from the site's Backups tab.

## SFTP

Turn on SFTP in a site's settings, then set a password on its Overview tab or
paste SSH keys. Log in as `site_<name>` on port 22; you land in the site's
folder and cannot leave it. Upload into `public`.

## Plugins

Plugins are built into the binary and switched on under **Platform → Plugins**.
Each declares its settings (secrets are never sent back to the browser) and may
add a page to the sidebar. To write one, implement `plugin.Plugin` in a package
under `internal/plugins`, add it to `plugins.All`, and for a page of its own
add a component to `pluginPages` in `web/src/pages/Plugins.tsx`. Its API is
served under `/api/p/<id>/` while it is on.

### Mailcow

Manages a [mailcow](https://mailcow.email) server: mail domains, mailboxes
(passwords made for you if left blank), aliases and catch-alls. Adding a domain:

1. creates it in mailcow and generates a 2048-bit DKIM key;
2. adds it to **Amazon SES** as a domain identity with Easy DKIM and a custom
   MAIL FROM (`bounce.<domain>`), so SPF and DKIM align for DMARC;
3. creates the DNS records in **Cloudflare** if you give it a token: MX, SPF,
   mailcow DKIM, the three SES DKIM CNAMEs, MAIL FROM MX and SPF, DMARC
   (only when there is none) and autodiscover/autoconfig. Records that already
   exist with other values are never changed; they show as clashes. Without a
   token the page lists the records to add yourself;
4. once SES has verified the domain (Berth checks every five minutes), turns on
   mailcow's **sender-dependent transport** to `email-smtp.<region>.amazonaws.com:587`
   for that domain. Until then mail goes out directly from mailcow, so nothing
   bounces while SES is still verifying. Each domain can be switched between
   SES and direct sending on its page.

Setup:

- mailcow: **System → Configuration → Access → API**, a read-write key, with
  Berth's IP in the allowed networks.
- AWS: an IAM user with `ses:CreateEmailIdentity`, `ses:GetEmailIdentity`,
  `ses:DeleteEmailIdentity`, `ses:PutEmailIdentityMailFromAttributes`,
  `ses:GetAccount` and `ses:SendRawEmail`. Berth derives the SES SMTP password
  from its secret key, so one key does both (or enter separate SMTP
  credentials). A new SES account is in the sandbox until AWS grants production
  access; the Test button tells you.
- Cloudflare (optional): a token with **Zone → DNS → Edit** on the mail domains.

### DNS hosting

Hosts customers' domains on your own [PowerDNS](https://www.powerdns.com)
nameservers. Switch it on for the accounts that may use it; they get a **DNS**
page to add their domains and edit records (A, AAAA, CNAME, MX, TXT, SRV, CAA,
and NS for subdomains). The SOA and the zone's own NS records are Berth's. A
customer can only add a domain nobody else hosts here, and never a public
suffix such as `co.uk`.

When a site's domain sits in one of its account's zones, Berth points it (and
its other domains) at this server with A and AAAA records, unless the name
already has a record. Domains a site drops lose the records Berth would have
made. Each site's Overview has a DNS card that checks where its domains point
and says what to change: the nameservers at the registrar, or A/AAAA records
for customers who keep their DNS elsewhere.

#### Setting up the nameservers

You need two nameservers on different networks, each reachable from the
internet on port 53 (TCP and UDP). A common layout is the primary in an LXC
next to Berth with port 53 forwarded to it (`ns1`), and a secondary on a small
VPS elsewhere (`ns2`).

1. **Primary.** On a Proxmox node:

   ```bash
   bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/berth/main/ct/pdns.sh)"
   ```

   or on any Debian 12/13 machine:

   ```bash
   curl -fsSLO https://raw.githubusercontent.com/joshuadouch/berth/main/install/pdns-install.sh
   PDNS_ROLE=primary PDNS_NS_NAME=ns1.example.net PDNS_API_ALLOW=<berth-ip> \
     PDNS_SECONDARY_IPS=<ns2-public-ip> bash pdns-install.sh
   ```

   It prints the API address and key for Berth.
2. **Secondary.** On the other machine:

   ```bash
   PDNS_ROLE=secondary PDNS_NS_NAME=ns2.example.net PDNS_PRIMARY_IP=<ns1-public-ip> bash pdns-install.sh
   ```

   It takes new zones from the primary's NOTIFY by itself, copies every change,
   and every 15 minutes drops zones the primary no longer serves. (Give Berth
   the secondary's API too, under **Secondary nameserver**, and Berth adds and
   removes zones there directly instead; then **Sync secondary** on the DNS page
   repairs any drift.)
3. **Names and glue.** In the zone of the domain your nameservers are named
   under (`example.net` above), add `ns1.example.net` and `ns2.example.net` as A
   (and AAAA) records with the public addresses. At that domain's registrar,
   register them as **glue records / host names** with the same addresses.
4. **Berth.** Plugins → DNS hosting: the primary's API address and key, the
   nameserver names (`ns1.example.net, ns2.example.net`), a hostmaster email,
   and this server's public IPv4/IPv6 for site records. **Test** checks both
   servers. Then choose the accounts it is on for.
5. **Firewall.** Port 53 TCP and UDP open to everyone on both servers; the API
   port (8081) only to Berth. The API speaks plain HTTP, so keep it on your LAN
   or a VPN.

Customers then set their domain's nameservers to `ns1.example.net` and
`ns2.example.net` at their registrar.

## Updating

Every change merged to `main` publishes a new release automatically
(`v0.1.<build>`). Update from the **System** page, or run this on the Proxmox
host:

```bash
pct exec <CTID> -- berth-update
```

It downloads the latest installer and binary, keeps sites, databases, backups
and settings, and restarts Berth. Sites keep serving throughout. To pin or roll
back a version, run `BERTH_VERSION=v0.1.7 berth-update`.

## Commands

```
berth serve     run the API, UI and scheduled jobs (the systemd service)
berth apply     write nginx and PHP-FPM config for every site
berth render    print that config without writing it
berth passwd    reset the admin password and turn off 2FA
berth version
```

Locked out? Run `berth passwd` in the container.

## Development

```bash
make build      # builds the UI and bin/berth
make test
```

`dry_run: true` in the config file writes nginx and PHP-FPM config but runs no
system commands.
