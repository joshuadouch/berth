# Berth

A small hosting control panel for your own WordPress and PHP sites, built to
sit behind [Halyard](https://github.com/joshuadouch/halyard). Berth is a single
Go binary with a web UI. It drives **nginx**, **PHP-FPM** (several versions side
by side, from packages.sury.org), **MariaDB** and **WP-CLI** on one Debian
machine.

There are no hosting customers or reseller accounts: one admin manages every
site.

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
- PHP version, memory, upload limit and workers per site; extra domains with an
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

Copied files in as root? Press **Fix permissions** on the site page. To bring in
a backup from elsewhere, copy it into `/var/backups/berth/<name>/` and restore it
from the site's Backups tab.

## SFTP

Turn on SFTP in a site's settings, then set a password on its Overview tab or
paste SSH keys. Log in as `site_<name>` on port 22; you land in the site's
folder and cannot leave it. Upload into `public`.

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
