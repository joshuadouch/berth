#!/usr/bin/env bash
# PowerDNS nameserver installer for Berth's DNS plugin. Debian 12 or 13, in a
# VM or LXC. Run it once on the primary and once on each secondary.
#
#   PDNS_ROLE=primary   bash pdns-install.sh
#   PDNS_ROLE=secondary bash pdns-install.sh
#
# The primary holds the zones and Berth edits them through its API. Secondaries
# pick up new zones by themselves (PowerDNS "autoprimary": the primary's
# NOTIFY creates the zone), copy every change, and drop zones the primary no
# longer serves. Running it again keeps the zones and the API key.
#
# Environment (prompted for when missing):
#   PDNS_ROLE          primary or secondary
#   PDNS_NS_NAME       this server's nameserver name, e.g. ns1.example.net
#   PDNS_SECONDARY_IPS (primary) the secondaries' public addresses, space separated
#   PDNS_PRIMARY_IP    (secondary) the address the primary sends from and serves transfers on
#   PDNS_API_ALLOW     who may use the API: Berth's address or subnet (primary
#                      default: 127.0.0.1; secondary default: none, API on localhost only)
#   PDNS_API_PORT      API port (default 8081)
set -euo pipefail

YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'; BOLD=$'\033[1m'
msg_info() { echo -e " ${YW}…${CL} $1"; }
msg_ok() { echo -e " ${GN}✓${CL} $1"; }
die() { echo -e " ${RD}✗${CL} $1" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "Run as root."
CODENAME="$(. /etc/os-release && echo "${VERSION_CODENAME:-}")"
case "$CODENAME" in bookworm|trixie) ;; *) die "This installer supports Debian 12 or 13.";; esac

ask() { # ask VAR "Question" default
  local var="$1" q="$2" def="${3:-}" val
  [[ -n "${!var:-}" ]] && return
  if [[ ! -t 0 || "${UNATTENDED:-0}" == "1" ]]; then printf -v "$var" '%s' "$def"; return; fi
  read -r -p " $q${def:+ [$def]}: " val
  printf -v "$var" '%s' "${val:-$def}"
}

ask PDNS_ROLE "Role (primary or secondary)" "primary"
[[ "$PDNS_ROLE" == primary || "$PDNS_ROLE" == secondary ]] || die "PDNS_ROLE must be primary or secondary"
ask PDNS_NS_NAME "This server's nameserver name (like ns1.example.net)" ""
[[ "$PDNS_NS_NAME" =~ ^[a-z0-9.-]+\.[a-z]+$ ]] || die "Give the nameserver name, like ns1.example.net"
PORT="${PDNS_API_PORT:-8081}"
if [[ "$PDNS_ROLE" == primary ]]; then
  ask PDNS_SECONDARY_IPS "Secondaries' public addresses (space separated, blank for none yet)" ""
  ask PDNS_API_ALLOW "Address or subnet of the Berth server (may use the API)" "127.0.0.1"
else
  ask PDNS_PRIMARY_IP "The primary's address" ""
  [[ -n "$PDNS_PRIMARY_IP" ]] || die "A secondary needs the primary's address"
  PDNS_API_ALLOW="${PDNS_API_ALLOW:-}"
fi

export DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 LC_ALL=C.UTF-8

# Port 53 must be free: Debian's systemd-resolved stub sits on it on some VPSes.
if systemctl is-active --quiet systemd-resolved 2>/dev/null; then
  msg_info "Moving systemd-resolved off port 53"
  mkdir -p /etc/systemd/resolved.conf.d
  printf '[Resolve]\nDNSStubListener=no\n' > /etc/systemd/resolved.conf.d/berth-pdns.conf
  ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
  systemctl restart systemd-resolved
fi

msg_info "Installing PowerDNS"
apt-get update -qq
# Keep Debian from starting pdns with its default bind backend mid-install.
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 0755 /usr/sbin/policy-rc.d
trap 'rm -f /usr/sbin/policy-rc.d' EXIT
apt-get install -y -qq pdns-server pdns-backend-sqlite3 sqlite3 bind9-dnsutils curl ca-certificates >/dev/null
rm -f /usr/sbin/policy-rc.d
msg_ok "PowerDNS $(dpkg-query -W -f='${Version}' pdns-server)"

DB=/var/lib/powerdns/pdns.sqlite3
if [[ ! -s "$DB" ]]; then
  msg_info "Creating the zone database"
  install -d -o pdns -g pdns -m 0750 /var/lib/powerdns
  sqlite3 "$DB" < /usr/share/doc/pdns-backend-sqlite3/schema.sqlite3.sql
  chown pdns:pdns "$DB" && chmod 0640 "$DB"
fi
msg_ok "Zones live in $DB"

KEYFILE=/etc/powerdns/berth-api-key
if [[ ! -s "$KEYFILE" ]]; then
  head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 40 > "$KEYFILE"
  chmod 0600 "$KEYFILE"
fi
KEY="$(cat "$KEYFILE")"

rm -f /etc/powerdns/pdns.d/bind.conf /etc/powerdns/pdns.d/bind.conf.dpkg-dist
WEB_ADDR=127.0.0.1
ALLOW="127.0.0.1,::1"
if [[ -n "$PDNS_API_ALLOW" && "$PDNS_API_ALLOW" != 127.0.0.1 ]]; then
  WEB_ADDR=0.0.0.0
  ALLOW="127.0.0.1,::1,$(echo "$PDNS_API_ALLOW" | tr ' ' ',')"
fi
{
  echo "# Written by Berth's pdns-install.sh; running it again rewrites this file."
  echo "launch=gsqlite3"
  echo "gsqlite3-database=$DB"
  echo "gsqlite3-pragma-foreign-keys=yes"
  echo "local-address=0.0.0.0, ::"
  echo "api=yes"
  echo "api-key=$KEY"
  echo "webserver=yes"
  echo "webserver-address=$WEB_ADDR"
  echo "webserver-port=$PORT"
  echo "webserver-allow-from=$ALLOW"
  echo "default-ttl=3600"
  echo "version-string=anonymous"
  if [[ "$PDNS_ROLE" == primary ]]; then
    echo "primary=yes"
    if [[ -n "${PDNS_SECONDARY_IPS:-}" ]]; then
      echo "allow-axfr-ips=127.0.0.1,::1,$(echo "$PDNS_SECONDARY_IPS" | tr ' ' ',')"
      echo "also-notify=$(echo "$PDNS_SECONDARY_IPS" | tr ' ' ',')"
    else
      echo "allow-axfr-ips=127.0.0.1,::1"
    fi
  else
    echo "secondary=yes"
    echo "autosecondary=yes"
    echo "allow-notify-from=${PDNS_PRIMARY_IP%:*}"
    echo "allow-axfr-ips=127.0.0.1,::1"
  fi
} > /etc/powerdns/pdns.d/berth.conf
chown root:pdns /etc/powerdns/pdns.d/berth.conf && chmod 0640 /etc/powerdns/pdns.d/berth.conf

systemctl enable pdns >/dev/null 2>&1 || true
systemctl restart pdns
sleep 1
systemctl is-active --quiet pdns || die "PowerDNS did not start: see journalctl -u pdns"
msg_ok "PowerDNS is running"

if [[ "$PDNS_ROLE" == secondary ]]; then
  # New zones arrive by NOTIFY from the primary.
  pdnsutil add-autoprimary "${PDNS_PRIMARY_IP%:*}" "$PDNS_NS_NAME" berth >/dev/null 2>&1 || true
  msg_ok "Accepting new zones from ${PDNS_PRIMARY_IP}"

  # Zones deleted on the primary are dropped here every 15 minutes.
  cat > /usr/local/sbin/pdns-prune-zones <<EOF
#!/bin/sh
# Remove secondary zones the primary no longer serves (written by pdns-install.sh).
PRIMARY="${PDNS_PRIMARY_IP%:*}"
zones=\$(pdnsutil list-all-zones secondary 2>/dev/null || pdnsutil list-all-zones slave 2>/dev/null)
for z in \$zones; do
  rc=\$(dig +norec +time=3 +tries=2 @"\$PRIMARY" "\$z" SOA | sed -n 's/.*status: \([A-Z]*\).*/\1/p')
  case "\$rc" in
    REFUSED|NOTAUTH|NXDOMAIN) pdnsutil delete-zone "\$z" >/dev/null && logger -t pdns-prune "removed \$z (primary answers \$rc)" ;;
  esac
done
EOF
  chmod 0755 /usr/local/sbin/pdns-prune-zones
  cat > /etc/systemd/system/pdns-prune-zones.service <<'EOF'
[Unit]
Description=Drop secondary zones the primary no longer serves
After=pdns.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/pdns-prune-zones
EOF
  cat > /etc/systemd/system/pdns-prune-zones.timer <<'EOF'
[Unit]
Description=Drop secondary zones the primary no longer serves

[Timer]
OnBootSec=5min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
EOF
  systemctl daemon-reload
  systemctl enable --now pdns-prune-zones.timer >/dev/null 2>&1
  msg_ok "Deleted zones are pruned every 15 minutes"
fi

IP="$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -1)"
echo
echo -e " ${GN}${BOLD}PowerDNS ${PDNS_ROLE} is ready as ${PDNS_NS_NAME}.${CL}"
if [[ "$PDNS_ROLE" == primary ]]; then
  echo -e "   In Berth, Plugins → DNS hosting:"
  echo -e "     API address: ${BL}http://${IP}:${PORT}${CL}"
  echo -e "     API key:     ${BL}${KEY}${CL}"
  echo -e "     Primary's address for zone transfers (only if you also give Berth a secondary's API): ${BL}${IP}${CL}"
  [[ "$WEB_ADDR" == 127.0.0.1 ]] && echo -e "   ${YW}The API only listens on localhost. Run again with PDNS_API_ALLOW=<Berth's address> so Berth can reach it.${CL}"
  echo -e "   Add a secondary later: run this again with PDNS_SECONDARY_IPS set."
else
  echo -e "   It copies zones from ${PDNS_PRIMARY_IP}. Make sure the primary lists this server in PDNS_SECONDARY_IPS."
  [[ "$WEB_ADDR" != 127.0.0.1 ]] && echo -e "   API (optional in Berth): ${BL}http://${IP}:${PORT}${CL} key ${BL}${KEY}${CL}"
fi
echo -e "   Open port ${BOLD}53 TCP and UDP${CL} to the world. Keep port ${PORT} closed to everyone but Berth."
echo
