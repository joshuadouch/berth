#!/usr/bin/env bash
# Berth nameserver: Proxmox VE helper script.
# Run on a Proxmox VE node shell. Creates small Debian 12 LXCs running
# PowerDNS for Berth's DNS plugin: a primary, a secondary, or both at once
# (two containers on this node, already linked to each other).
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/berth/main/ct/pdns.sh)"
#
# Every prompt can be preset with an environment variable for unattended use:
#   PDNS_ROLE (both, primary or secondary)
#   CTID CT_HOSTNAME NET (dhcp or CIDR) PDNS_NS_NAME     first (or only) container
#   CTID2 CT_HOSTNAME2 NET2 PDNS_NS_NAME2                 second container (both)
#   GATE DISK_SIZE CORE_COUNT RAM_SIZE BRIDGE STORAGE TEMPLATE_STORAGE
#   ROOT_PASSWORD SSH_KEY_FILE (path to public keys for root)
#   PDNS_SECONDARY_IPS PDNS_PRIMARY_IP PDNS_API_ALLOW
#   UNATTENDED=1 (accept defaults without prompts)
set -euo pipefail

APP="Berth nameserver"
REPO="${BERTH_REPO:-joshuadouch/berth}"
BRANCH="${BERTH_BRANCH:-main}"

YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'
msg_info() { echo -e " ${YW}…${CL} $1"; }
msg_ok() { echo -e " ${GN}✓${CL} $1"; }
die() { echo -e " ${RD}✗${CL} $1" >&2; exit 1; }

command -v pveversion >/dev/null || die "Run this on a Proxmox VE host."
[[ $EUID -eq 0 ]] || die "Run as root."
clear || true
echo -e " PowerDNS nameservers for Berth\n"

interactive() { [[ "${UNATTENDED:-0}" != "1" ]] && command -v whiptail >/dev/null; }

ask() { # ask VAR "Question" default
  local var="$1" q="$2" def="$3" val
  if [[ -n "${!var:-}" ]]; then return; fi
  if ! interactive; then printf -v "$var" '%s' "$def"; return; fi
  val=$(whiptail --backtitle "Berth" --title "$APP LXC" --inputbox "$q" 10 72 "$def" 3>&1 1>&2 2>&3) || die "Cancelled"
  printf -v "$var" '%s' "$val"
}

ask_password() { # ask_password VAR "Question"
  local var="$1" q="$2" a b
  if [[ -n "${!var+x}" ]]; then return; fi
  if ! interactive; then printf -v "$var" '%s' ""; return; fi
  while true; do
    a=$(whiptail --backtitle "Berth" --title "$APP LXC" --passwordbox "$q" 9 72 3>&1 1>&2 2>&3) || die "Cancelled"
    [[ -z "$a" ]] && break
    b=$(whiptail --backtitle "Berth" --title "$APP LXC" --passwordbox "Repeat the root password" 9 72 3>&1 1>&2 2>&3) || die "Cancelled"
    [[ "$a" == "$b" ]] && break
    whiptail --backtitle "Berth" --title "$APP LXC" --msgbox "Passwords did not match." 8 40
  done
  printf -v "$var" '%s' "$a"
}

if [[ -z "${PDNS_ROLE:-}" ]]; then
  if interactive; then
    PDNS_ROLE=$(whiptail --backtitle "Berth" --title "$APP LXC" --menu "What should this node run?" 14 72 3 \
      both "Primary and secondary, two containers on this node" \
      primary "Primary only (the secondary is elsewhere)" \
      secondary "Secondary only (the primary is elsewhere)" 3>&1 1>&2 2>&3) || die "Cancelled"
  else
    PDNS_ROLE=both
  fi
fi
[[ "$PDNS_ROLE" == both || "$PDNS_ROLE" == primary || "$PDNS_ROLE" == secondary ]] || die "Role must be both, primary or secondary"

# The first container is the primary (both, primary) or the secondary.
NEXT="$(pvesh get /cluster/nextid)"
if [[ "$PDNS_ROLE" == secondary ]]; then DEF_HOST=ns2; else DEF_HOST=ns1; fi
OF=""; [[ "$PDNS_ROLE" == both ]] && OF=" of the primary"
ask CTID "Container ID${OF}" "$NEXT"
[[ "$CTID" =~ ^[0-9]+$ ]] || die "Invalid container ID"
ask CT_HOSTNAME "Hostname${OF}" "$DEF_HOST"
ask PDNS_NS_NAME "Public nameserver name of ${CT_HOSTNAME} (like ${DEF_HOST}.example.net)" ""
[[ -n "$PDNS_NS_NAME" ]] || die "The nameserver name is needed"
ask NET "IPv4 of ${CT_HOSTNAME} (CIDR like 10.0.10.53/24). Use a fixed address: port 53 is forwarded to it." "dhcp"
if [[ "$PDNS_ROLE" == both ]]; then
  ask CTID2 "Container ID of the secondary" "$((CTID + 1))"
  [[ "$CTID2" =~ ^[0-9]+$ && "$CTID2" != "$CTID" ]] || die "Invalid container ID for the secondary"
  ask CT_HOSTNAME2 "Hostname of the secondary" "ns2"
  ask PDNS_NS_NAME2 "Public nameserver name of ${CT_HOSTNAME2}" "${PDNS_NS_NAME/#ns1./ns2.}"
  [[ -n "$PDNS_NS_NAME2" && "$PDNS_NS_NAME2" != "$PDNS_NS_NAME" ]] || die "The secondary needs its own nameserver name"
  ask NET2 "IPv4 of ${CT_HOSTNAME2} (CIDR). Use a fixed address: port 53 is forwarded to it." "dhcp"
fi
if [[ "$NET" != "dhcp" || "${NET2:-dhcp}" != "dhcp" ]]; then ask GATE "Gateway" ""; fi
ask CORE_COUNT "CPU cores (each container)" "1"
ask RAM_SIZE "Memory in MiB (each container)" "512"
ask DISK_SIZE "Disk size in GiB (each container)" "4"
ask BRIDGE "Bridge" "vmbr0"
case "$PDNS_ROLE" in
  both)
    ask PDNS_API_ALLOW "Berth's address or subnet (may use the primary's API)" "" ;;
  primary)
    ask PDNS_API_ALLOW "Berth's address or subnet (may use the API)" ""
    ask PDNS_SECONDARY_IPS "Secondaries' addresses, space separated (blank: none yet)" "" ;;
  secondary)
    ask PDNS_PRIMARY_IP "The primary's address" ""
    [[ -n "$PDNS_PRIMARY_IP" ]] || die "A secondary needs the primary's address" ;;
esac
ask_password ROOT_PASSWORD "Root password for the containers (blank: no password, console logs in automatically)"
DEFAULT_KEYS=""
[[ -s /root/.ssh/authorized_keys ]] && DEFAULT_KEYS="/root/.ssh/authorized_keys"
ask SSH_KEY_FILE "SSH public keys for root (file path, blank for none)" "$DEFAULT_KEYS"
ask STORAGE "Storage for the root disks" "$(pvesm status -content rootdir | awk 'NR==2{print $1}')"
ask TEMPLATE_STORAGE "Storage for templates" "$(pvesm status -content vztmpl | awk 'NR==2{print $1}')"

pct status "$CTID" >/dev/null 2>&1 && die "Container $CTID already exists"
[[ "$PDNS_ROLE" != both ]] || ! pct status "$CTID2" >/dev/null 2>&1 || die "Container $CTID2 already exists"
[[ -n "$STORAGE" && -n "$TEMPLATE_STORAGE" ]] || die "No suitable storage found"
[[ -z "${SSH_KEY_FILE:-}" || -r "$SSH_KEY_FILE" ]] || die "SSH key file $SSH_KEY_FILE not found"
ACCESS=()
[[ -n "${ROOT_PASSWORD:-}" ]] && ACCESS+=(--password "$ROOT_PASSWORD")
[[ -n "${SSH_KEY_FILE:-}" ]] && ACCESS+=(--ssh-public-keys "$SSH_KEY_FILE")

msg_info "Finding the Debian 12 template"
pveam update >/dev/null 2>&1 || true
TEMPLATE="$(pveam available --section system | awk '/debian-12-standard/ {print $2}' | sort -V | tail -1)"
[[ -n "$TEMPLATE" ]] || die "No debian-12-standard template available"
if ! pveam list "$TEMPLATE_STORAGE" | grep -q "$TEMPLATE"; then
  pveam download "$TEMPLATE_STORAGE" "$TEMPLATE" >/dev/null
fi
msg_ok "Template $TEMPLATE"

# Locate the installer: next to this script when run from a clone, else GitHub.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo /nonexistent)"
INSTALLER="$(mktemp)"
trap 'rm -f "$INSTALLER"' EXIT
if [[ -f "$SCRIPT_DIR/../install/pdns-install.sh" ]]; then
  cp "$SCRIPT_DIR/../install/pdns-install.sh" "$INSTALLER"
else
  curl -fsSL "https://raw.githubusercontent.com/${REPO}/${BRANCH}/install/pdns-install.sh" -o "$INSTALLER" \
    || die "Could not download the installer"
fi

create_ct() { # create_ct ID HOSTNAME NET ROLE NSNAME
  local id="$1" host="$2" net="$3" role="$4" ns="$5" net0
  net0="name=eth0,bridge=${BRIDGE},ip=${net}"
  [[ "$net" != "dhcp" && -n "${GATE:-}" ]] && net0+=",gw=${GATE}"
  msg_info "Creating LXC $id ($host)"
  pct create "$id" "${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}" \
    --hostname "$host" --cores "$CORE_COUNT" --memory "$RAM_SIZE" --swap 256 \
    --rootfs "${STORAGE}:${DISK_SIZE}" --net0 "$net0" "${ACCESS[@]}" \
    --unprivileged 1 --features nesting=1 --onboot 1 --tags "berth;dns" \
    --description "PowerDNS ${role} (${ns}) for Berth. https://github.com/${REPO}" >/dev/null
  msg_ok "Created LXC $id ($host)"

  msg_info "Starting $host and waiting for network"
  pct start "$id"
  for _ in $(seq 1 30); do
    if pct exec "$id" -- getent hosts deb.debian.org >/dev/null 2>&1; then break; fi
    sleep 2
  done
  pct exec "$id" -- getent hosts deb.debian.org >/dev/null 2>&1 || die "Container $id has no working network/DNS"
  msg_ok "$host is up"

  if [[ -z "${ROOT_PASSWORD:-}" ]]; then
    # No password: log the Proxmox console straight in as root.
    pct exec "$id" -- bash -c 'mkdir -p /etc/systemd/system/container-getty@1.service.d && cat > /etc/systemd/system/container-getty@1.service.d/autologin.conf <<EOF
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear --keep-baud tty%I 115200,38400,9600 \$TERM
EOF
systemctl daemon-reload && systemctl restart container-getty@1.service' || true
  fi
  pct push "$id" "$INSTALLER" /root/pdns-install.sh --perms 0755
}

ct_ip() { # the container's IPv4 address on its bridge
  pct exec "$1" -- sh -c "ip -4 -o addr show dev eth0 scope global | awk '{print \$4}' | cut -d/ -f1 | head -1"
}

install_pdns() { # install_pdns ID ROLE NSNAME SECONDARY_IPS PRIMARY_IP
  local id="$1" allow="${PDNS_API_ALLOW:-}"
  # Berth only needs the primary's API; a secondary made here keeps its own on localhost.
  [[ "$PDNS_ROLE" == both && "$2" == secondary ]] && allow=""
  msg_info "Installing PowerDNS ${2} in LXC $id"
  echo
  pct exec "$id" -- env LANG=C.UTF-8 LC_ALL=C.UTF-8 UNATTENDED=1 PDNS_ROLE="$2" PDNS_NS_NAME="$3" \
    PDNS_SECONDARY_IPS="$4" PDNS_PRIMARY_IP="$5" PDNS_API_ALLOW="$allow" bash /root/pdns-install.sh
}

hint() { # hint ID ROLE NSNAME SECONDARY_IPS PRIMARY_IP
  echo -e "   ${CT_HOSTNAME_OF[$1]}: ${BL}pct enter $1${CL}. To change settings later, run inside it:"
  if [[ "$2" == primary ]]; then
    echo -e "   ${BL}PDNS_ROLE=primary PDNS_NS_NAME=$3 PDNS_SECONDARY_IPS=\"$4\" PDNS_API_ALLOW=\"${PDNS_API_ALLOW:-}\" bash /root/pdns-install.sh${CL}"
  else
    echo -e "   ${BL}PDNS_ROLE=secondary PDNS_NS_NAME=$3 PDNS_PRIMARY_IP=$5 bash /root/pdns-install.sh${CL}"
  fi
}

declare -A CT_HOSTNAME_OF=([$CTID]="$CT_HOSTNAME")
case "$PDNS_ROLE" in
  both)
    CT_HOSTNAME_OF[$CTID2]="$CT_HOSTNAME2"
    # Make both first: each one's config needs the other's address.
    create_ct "$CTID" "$CT_HOSTNAME" "$NET" primary "$PDNS_NS_NAME"
    create_ct "$CTID2" "$CT_HOSTNAME2" "$NET2" secondary "$PDNS_NS_NAME2"
    IP1="$(ct_ip "$CTID")"; IP2="$(ct_ip "$CTID2")"
    [[ -n "$IP1" && -n "$IP2" ]] || die "Could not read the containers' addresses"
    msg_ok "Primary ${IP1}, secondary ${IP2}"
    # Secondary first, so it already accepts the primary's NOTIFYs.
    install_pdns "$CTID2" secondary "$PDNS_NS_NAME2" "" "$IP1"
    install_pdns "$CTID" primary "$PDNS_NS_NAME" "$IP2" ""
    hint "$CTID" primary "$PDNS_NS_NAME" "$IP2" ""
    hint "$CTID2" secondary "$PDNS_NS_NAME2" "" "$IP1"
    echo
    echo -e "   ${YW}Forward port 53 TCP and UDP to ${IP1} (${PDNS_NS_NAME}) and to ${IP2} (${PDNS_NS_NAME2}).${CL}"
    echo -e "   ${YW}That needs two public addresses. With only one, forward port 53 to the primary and point both names at it;${CL}"
    echo -e "   ${YW}the secondary then stays a ready copy you can switch to.${CL}"
    [[ "$NET" == dhcp || "$NET2" == dhcp ]] && echo -e "   ${YW}DHCP was used: reserve these addresses, or the two servers lose each other when they change.${CL}"
    ;;
  primary)
    create_ct "$CTID" "$CT_HOSTNAME" "$NET" primary "$PDNS_NS_NAME"
    install_pdns "$CTID" primary "$PDNS_NS_NAME" "${PDNS_SECONDARY_IPS:-}" ""
    hint "$CTID" primary "$PDNS_NS_NAME" "${PDNS_SECONDARY_IPS:-}" ""
    ;;
  secondary)
    create_ct "$CTID" "$CT_HOSTNAME" "$NET" secondary "$PDNS_NS_NAME"
    install_pdns "$CTID" secondary "$PDNS_NS_NAME" "" "$PDNS_PRIMARY_IP"
    hint "$CTID" secondary "$PDNS_NS_NAME" "" "$PDNS_PRIMARY_IP"
    ;;
esac
echo
