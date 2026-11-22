#!/usr/bin/env bash
# Berth nameserver: Proxmox VE helper script.
# Run on a Proxmox VE node shell. Creates a small Debian 12 LXC running
# PowerDNS as the primary (or a secondary) nameserver for Berth's DNS plugin.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/berth/main/ct/pdns.sh)"
#
# Every prompt can be preset with an environment variable for unattended use:
#   CTID CT_HOSTNAME DISK_SIZE CORE_COUNT RAM_SIZE BRIDGE NET (dhcp or CIDR) GATE
#   STORAGE TEMPLATE_STORAGE ROOT_PASSWORD SSH_KEY_FILE (path to public keys for root)
#   PDNS_ROLE PDNS_NS_NAME PDNS_SECONDARY_IPS PDNS_PRIMARY_IP PDNS_API_ALLOW
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
echo -e " PowerDNS nameserver for Berth\n"

ask() { # ask VAR "Question" default
  local var="$1" q="$2" def="$3" val
  if [[ -n "${!var:-}" ]]; then return; fi
  if [[ "${UNATTENDED:-0}" == "1" ]] || ! command -v whiptail >/dev/null; then
    printf -v "$var" '%s' "$def"; return
  fi
  val=$(whiptail --backtitle "Berth" --title "$APP LXC" --inputbox "$q" 9 70 "$def" 3>&1 1>&2 2>&3) || die "Cancelled"
  printf -v "$var" '%s' "$val"
}

ask_password() { # ask_password VAR "Question"
  local var="$1" q="$2" a b
  if [[ -n "${!var+x}" ]]; then return; fi
  if [[ "${UNATTENDED:-0}" == "1" ]] || ! command -v whiptail >/dev/null; then
    printf -v "$var" '%s' ""; return
  fi
  while true; do
    a=$(whiptail --backtitle "Berth" --title "$APP LXC" --passwordbox "$q" 9 70 3>&1 1>&2 2>&3) || die "Cancelled"
    [[ -z "$a" ]] && break
    b=$(whiptail --backtitle "Berth" --title "$APP LXC" --passwordbox "Repeat the root password" 9 70 3>&1 1>&2 2>&3) || die "Cancelled"
    [[ "$a" == "$b" ]] && break
    whiptail --backtitle "Berth" --title "$APP LXC" --msgbox "Passwords did not match." 8 40
  done
  printf -v "$var" '%s' "$a"
}

ask PDNS_ROLE "Role: primary or secondary" "primary"
ask CTID "Container ID" "$(pvesh get /cluster/nextid)"
ask CT_HOSTNAME "Hostname" "ns1"
ask PDNS_NS_NAME "Public nameserver name of this server (like ns1.example.net)" ""
ask CORE_COUNT "CPU cores" "1"
ask RAM_SIZE "Memory (MiB)" "512"
ask DISK_SIZE "Disk size (GiB)" "4"
ask BRIDGE "Bridge" "vmbr0"
ask NET "IPv4 (CIDR like 10.0.10.53/24). Use a fixed address: port 53 is forwarded to it." "dhcp"
if [[ "$NET" != "dhcp" ]]; then ask GATE "Gateway" ""; fi
if [[ "$PDNS_ROLE" == "primary" ]]; then
  ask PDNS_API_ALLOW "Berth's address or subnet (may use the API)" ""
  ask PDNS_SECONDARY_IPS "Secondaries' public addresses, space separated (blank: none yet)" ""
else
  ask PDNS_PRIMARY_IP "The primary's address" ""
fi
ask_password ROOT_PASSWORD "Root password for the container (blank: no password, console logs in automatically)"
DEFAULT_KEYS=""
[[ -s /root/.ssh/authorized_keys ]] && DEFAULT_KEYS="/root/.ssh/authorized_keys"
ask SSH_KEY_FILE "SSH public keys for root (file path, blank for none)" "$DEFAULT_KEYS"
ask STORAGE "Storage for the root disk" "$(pvesm status -content rootdir | awk 'NR==2{print $1}')"
ask TEMPLATE_STORAGE "Storage for templates" "$(pvesm status -content vztmpl | awk 'NR==2{print $1}')"

[[ "$PDNS_ROLE" == primary || "$PDNS_ROLE" == secondary ]] || die "Role must be primary or secondary"
[[ -n "$PDNS_NS_NAME" ]] || die "The nameserver name is needed"
[[ "$PDNS_ROLE" == primary || -n "${PDNS_PRIMARY_IP:-}" ]] || die "A secondary needs the primary's address"
[[ "$CTID" =~ ^[0-9]+$ ]] || die "Invalid container ID"
pct status "$CTID" >/dev/null 2>&1 && die "Container $CTID already exists"
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

NET0="name=eth0,bridge=${BRIDGE},ip=${NET}"
[[ "$NET" != "dhcp" && -n "${GATE:-}" ]] && NET0+=",gw=${GATE}"

msg_info "Creating LXC $CTID"
pct create "$CTID" "${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}" \
  --hostname "$CT_HOSTNAME" --cores "$CORE_COUNT" --memory "$RAM_SIZE" --swap 256 \
  --rootfs "${STORAGE}:${DISK_SIZE}" --net0 "$NET0" "${ACCESS[@]}" \
  --unprivileged 1 --features nesting=1 --onboot 1 --tags "berth;dns" \
  --description "PowerDNS ${PDNS_ROLE} (${PDNS_NS_NAME}) for Berth. https://github.com/${REPO}" >/dev/null
msg_ok "Created LXC $CTID"

msg_info "Starting container and waiting for network"
pct start "$CTID"
for _ in $(seq 1 30); do
  if pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1; then break; fi
  sleep 2
done
pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1 || die "Container has no working network/DNS"
msg_ok "Network is up"

if [[ -z "${ROOT_PASSWORD:-}" ]]; then
  # No password: log the Proxmox console straight in as root.
  pct exec "$CTID" -- bash -c 'mkdir -p /etc/systemd/system/container-getty@1.service.d && cat > /etc/systemd/system/container-getty@1.service.d/autologin.conf <<EOF
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear --keep-baud tty%I 115200,38400,9600 \$TERM
EOF
systemctl daemon-reload && systemctl restart container-getty@1.service' || true
fi

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
pct push "$CTID" "$INSTALLER" /root/pdns-install.sh --perms 0755
ENVS=("LANG=C.UTF-8" "LC_ALL=C.UTF-8" "UNATTENDED=1" "PDNS_ROLE=${PDNS_ROLE}" "PDNS_NS_NAME=${PDNS_NS_NAME}"
  "PDNS_API_ALLOW=${PDNS_API_ALLOW:-}" "PDNS_SECONDARY_IPS=${PDNS_SECONDARY_IPS:-}" "PDNS_PRIMARY_IP=${PDNS_PRIMARY_IP:-}")

msg_info "Installing PowerDNS inside the container"
echo
pct exec "$CTID" -- env "${ENVS[@]}" bash /root/pdns-install.sh
echo -e "   Shell: ${BL}pct enter ${CTID}${CL}. To change settings later, run inside it:"
echo -e "   ${BL}PDNS_ROLE=${PDNS_ROLE} PDNS_NS_NAME=${PDNS_NS_NAME} PDNS_SECONDARY_IPS=\"...\" bash /root/pdns-install.sh${CL}"
echo
