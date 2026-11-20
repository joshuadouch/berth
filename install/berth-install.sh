#!/usr/bin/env bash
# Berth installer for Debian 12 (bookworm) and 13 (trixie), in a VM or LXC.
#
# Installs nginx, MariaDB, PHP-FPM (from packages.sury.org), WP-CLI and the
# Berth service. Running it again updates Berth and keeps sites and settings.
#
#   bash berth-install.sh
#
# Environment (all optional):
#   BERTH_VERSION   release tag to install (default: latest)
#   BERTH_REPO      GitHub repo (default: joshuadouch/berth)
#   BERTH_BINARY    path to a berth binary already on this machine (skips download)
#   BERTH_PHP       PHP version to install on first run (default: 8.4)
#   BERTH_LISTEN    management address (default :8443)
#   BERTH_NO_SFTP=1 do not set up SFTP logins for sites
set -euo pipefail

REPO="${BERTH_REPO:-joshuadouch/berth}"
VERSION="${BERTH_VERSION:-latest}"
PHPV="${BERTH_PHP:-8.4}"
LISTEN="${BERTH_LISTEN:-:8443}"

YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'
msg_info() { echo -e " ${YW}…${CL} $1"; }
msg_ok() { echo -e " ${GN}✓${CL} $1"; }
msg_err() { echo -e " ${RD}✗${CL} $1" >&2; }
die() { msg_err "$1"; exit 1; }

[[ $EUID -eq 0 ]] || die "Run as root."
[[ -r /etc/os-release ]] || die "Cannot detect the OS."
# Read os-release in a subshell: it defines VERSION and others that would
# overwrite this script's variables.
OS_ID="$(. /etc/os-release && echo "${ID:-}")"
OS_NAME="$(. /etc/os-release && echo "${PRETTY_NAME:-unknown}")"
CODENAME="$(. /etc/os-release && echo "${VERSION_CODENAME:-}")"
[[ "$OS_ID" == "debian" ]] || die "Berth's installer supports Debian 12 or 13 (found ${OS_NAME})."
case "$CODENAME" in bookworm|trixie) ;; *) die "Unsupported Debian release: ${CODENAME:-unknown}";; esac
[[ "$PHPV" =~ ^[0-9]\.[0-9]$ ]] || die "BERTH_PHP must look like 8.4"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Unsupported CPU architecture $(uname -m)";;
esac

export DEBIAN_FRONTEND=noninteractive
export LANG=C.UTF-8 LC_ALL=C.UTF-8

# Fail instead of hanging when a mirror is slow, and stay on IPv4 when this
# machine has an IPv6 address but no IPv6 route out (common on LXC with SLAAC).
cat > /etc/apt/apt.conf.d/90berth <<'APT'
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
Acquire::Retries "3";
APT
CURL_OPTS=(--connect-timeout 20 --retry 3)
if ! ip -6 route show default 2>/dev/null | grep -q .; then
  echo 'Acquire::ForceIPv4 "true";' >> /etc/apt/apt.conf.d/90berth
  CURL_OPTS+=(-4)
fi

UPGRADE=0
[[ -x /usr/local/bin/berth ]] && UPGRADE=1

msg_info "Updating package lists"
apt-get update -q
msg_ok "Package lists updated"
msg_info "Installing nginx, MariaDB and tools"
apt-get install -y -q curl ca-certificates gnupg procps iproute2 jq nginx mariadb-server mariadb-client \
  openssh-server logrotate unzip
msg_ok "Installed $(nginx -v 2>&1 | cut -d' ' -f3) and MariaDB $(mariadb --version | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"

if [[ ! -f /etc/apt/sources.list.d/php.list ]]; then
  msg_info "Adding the PHP repository (packages.sury.org)"
  curl "${CURL_OPTS[@]}" -fsSL -o /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb
  dpkg -i /tmp/debsuryorg-archive-keyring.deb >/dev/null
  rm -f /tmp/debsuryorg-archive-keyring.deb
  echo "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ ${CODENAME} main" \
    > /etc/apt/sources.list.d/php.list
  apt-get update -q
  msg_ok "PHP repository added"
fi

if [[ $UPGRADE == 0 ]] || ! ls /usr/sbin/php-fpm* >/dev/null 2>&1; then
  msg_info "Installing PHP ${PHPV}"
  apt-get install -y -q "php${PHPV}-fpm" "php${PHPV}-cli" "php${PHPV}-mysql" "php${PHPV}-curl" "php${PHPV}-gd" \
    "php${PHPV}-intl" "php${PHPV}-mbstring" "php${PHPV}-xml" "php${PHPV}-zip" "php${PHPV}-bcmath" "php${PHPV}-soap" "php${PHPV}-gmp"
  apt-get install -y -q "php${PHPV}-imagick" "php${PHPV}-opcache" || true
  systemctl enable -q --now "php${PHPV}-fpm"
  msg_ok "Installed PHP ${PHPV}"
fi

# Extensions added to Berth's list later (gmp, for apps such as Invoice
# Ninja) go onto PHP versions installed before.
for FPM in /usr/sbin/php-fpm[0-9]*; do
  [[ -e "$FPM" ]] || continue
  V="${FPM#/usr/sbin/php-fpm}"
  if ! dpkg -s "php${V}-gmp" >/dev/null 2>&1; then
    apt-get install -y -q "php${V}-gmp" >/dev/null 2>&1 && systemctl restart "php${V}-fpm" || true
  fi
done

msg_info "Installing WP-CLI"
WP_TMP="$(mktemp)"
curl "${CURL_OPTS[@]}" -fsSL -o "$WP_TMP" https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar
WP_SUM="$(curl "${CURL_OPTS[@]}" -fsSL https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar.sha512 | awk '{print $1}')"
echo "${WP_SUM}  ${WP_TMP}" | sha512sum -c --quiet - || die "WP-CLI download did not match its checksum"
install -m 0755 "$WP_TMP" /usr/local/bin/wp
rm -f "$WP_TMP"
msg_ok "Installed WP-CLI $(php /usr/local/bin/wp --allow-root --version 2>/dev/null | awk '{print $2}')"

msg_info "Installing Berth"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
if [[ -n "${BERTH_BINARY:-}" ]]; then
  [[ -f "$BERTH_BINARY" ]] || die "BERTH_BINARY=$BERTH_BINARY does not exist"
  cp "$BERTH_BINARY" "$TMP"
else
  ASSET="berth-linux-${ARCH}"
  URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
  [[ "$VERSION" != "latest" ]] && URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
  curl "${CURL_OPTS[@]}" -fsSL "$URL" -o "$TMP" || die "Download failed from $URL"
fi
OLD_VERSION=""
[[ $UPGRADE == 1 ]] && OLD_VERSION="$(/usr/local/bin/berth version 2>/dev/null || true)"
install -m 0755 "$TMP" /usr/local/bin/berth
NEW_VERSION="$(/usr/local/bin/berth version)"
if [[ $UPGRADE == 1 ]]; then
  msg_ok "Updated Berth ${OLD_VERSION:-unknown} -> ${NEW_VERSION}"
else
  msg_ok "Installed Berth ${NEW_VERSION}"
fi

# berth-update re-runs the latest installer from GitHub. Sites, databases,
# backups and settings are kept; only packages, the binary and service files change.
cat > /usr/local/bin/berth-update <<UPD
#!/usr/bin/env bash
# Update Berth to the latest release, or to BERTH_VERSION=vX.Y.Z.
set -euo pipefail
export BERTH_REPO="\${BERTH_REPO:-${REPO}}"
exec bash -c "\$(curl -fsSL "https://raw.githubusercontent.com/\${BERTH_REPO}/main/install/berth-install.sh")"
UPD
chmod 0755 /usr/local/bin/berth-update
# `pct exec` runs with a minimal PATH (/bin:/usr/bin), so link both commands there.
ln -sf /usr/local/bin/berth-update /usr/bin/berth-update
ln -sf /usr/local/bin/berth /usr/bin/berth

msg_info "Configuring the system"
install -d -m 0750 /etc/berth /var/lib/berth
install -d -m 0751 /srv/sites
install -d -m 0750 /var/log/berth
mkdir -p /var/backups/berth && chmod 0700 /var/backups/berth 2>/dev/null || true
install -d -m 0755 /etc/nginx/berth /etc/nginx/berth/sites

if [[ ! -f /etc/berth/berth.yaml ]]; then
  IPV6=true
  [[ -f /proc/net/if_inet6 ]] || IPV6=false
  cat > /etc/berth/berth.yaml <<CFG
# Berth machine configuration. Sites and settings live in the database and are
# edited in the web UI; this file only describes this machine.
listen: "${LISTEN}"
data_dir: /var/lib/berth
sites_dir: /srv/sites
log_dir: /var/log/berth

nginx:
  conf_dir: /etc/nginx/berth
  reload_cmd: systemctl reload nginx
  ipv6: ${IPV6}

sftp:
  enabled: $([[ "${BERTH_NO_SFTP:-0}" == "1" ]] && echo false || echo true)
CFG
  chmod 0640 /etc/berth/berth.yaml
fi

# nginx: Berth owns /etc/nginx/berth. The globs keep nginx valid before the
# first apply.
cat > /etc/nginx/conf.d/berth.conf <<'NGX'
# Managed by the Berth installer.
server_tokens off;
include /etc/nginx/berth/http*.conf;
include /etc/nginx/berth/sites/*.conf;
NGX
rm -f /etc/nginx/sites-enabled/default

# SFTP: site users in berth-sftp are locked into their site folder.
if [[ "${BERTH_NO_SFTP:-0}" != "1" ]]; then
  getent group berth-sftp >/dev/null || groupadd --system berth-sftp
  install -d -m 0755 /etc/ssh/berth_keys
  cat > /etc/ssh/sshd_config.d/50-berth-sftp.conf <<'SSH'
# Managed by the Berth installer: SFTP for sites, chrooted to /srv/sites/<site>.
Match Group berth-sftp
    ChrootDirectory %h
    ForceCommand internal-sftp -u 0022
    AuthorizedKeysFile /etc/ssh/berth_keys/%u
    PasswordAuthentication yes
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTunnel no
SSH
  if sshd -t 2>/dev/null; then
    systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
  else
    rm -f /etc/ssh/sshd_config.d/50-berth-sftp.conf
    msg_err "sshd rejected the SFTP config; SFTP is off (sites still work)"
  fi
fi

cat > /etc/logrotate.d/berth <<'ROT'
/var/log/berth/*/*.log {
    daily
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    sharedscripts
    postrotate
        [ -s /run/nginx.pid ] && kill -USR1 "$(cat /run/nginx.pid)"
    endscript
}
/srv/sites/*/logs/*.log {
    weekly
    rotate 8
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    su root root
}
ROT

cat > /etc/systemd/system/berth.service <<'UNIT'
[Unit]
Description=Berth hosting control panel
Documentation=https://github.com/joshuadouch/berth
After=network-online.target nginx.service mariadb.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/berth serve -config /etc/berth/berth.yaml
Restart=on-failure
RestartSec=3
# Berth creates users, databases and config files, so it runs as root.
UMask=0027
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
msg_ok "System configured"

msg_info "Starting services"
systemctl enable -q --now mariadb nginx
/usr/local/bin/berth apply -config /etc/berth/berth.yaml >/dev/null
systemctl reload nginx
systemctl enable -q --now berth
if [[ $UPGRADE == 1 ]]; then systemctl restart berth; fi
sleep 2
systemctl is-active -q berth || { journalctl -u berth -n 30 --no-pager; die "Berth failed to start"; }
msg_ok "Berth is running"

IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") print $(i+1)}' | head -1)"
PORT="${LISTEN##*:}"
echo
if [[ $UPGRADE == 1 ]]; then
  echo -e " ${GN}Berth ${NEW_VERSION} is running.${CL} Sites, databases and settings were kept."
  echo
  exit 0
fi
echo -e " ${GN}Berth is ready.${CL}"
echo -e "   Open ${BL}https://${IP:-<this-machine>}:${PORT}${CL} and create the admin account."
echo -e "   Sites answer on ${BL}http://${IP:-<this-machine>}:80${CL}; point a Halyard pool at it"
echo -e "   (health check path /berth-health) and add this server's Halyard nodes as trusted proxies."
echo -e "   To update later, run ${BL}berth-update${CL}."
echo
