#!/usr/bin/env bash
# Berth: Proxmox VE helper script.
# Run on a Proxmox VE node shell. Creates a Debian 12 LXC and installs Berth
# (nginx, PHP-FPM, MariaDB, WP-CLI) inside it.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/joshuadouch/berth/main/ct/berth.sh)"
#
# Every prompt can be preset with an environment variable for unattended use:
#   CTID CT_HOSTNAME DISK_SIZE CORE_COUNT RAM_SIZE BRIDGE NET (dhcp or CIDR) GATE
#   STORAGE TEMPLATE_STORAGE ROOT_PASSWORD SSH_KEY_FILE (path to public keys for root)
#   BACKUP_MOUNT (a folder on this Proxmox host to keep backups in)
#   BERTH_PHP BERTH_VERSION BERTH_BINARY
#   UNATTENDED=1 (accept defaults without prompts)
set -euo pipefail

APP="Berth"
REPO="${BERTH_REPO:-joshuadouch/berth}"
BRANCH="${BERTH_BRANCH:-main}"

YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'; BOLD=$'\033[1m'
msg_info() { echo -e " ${YW}…${CL} $1"; }
msg_ok() { echo -e " ${GN}✓${CL} $1"; }
die() { echo -e " ${RD}✗${CL} $1" >&2; exit 1; }

header() {
  clear || true
  cat <<'EOF'
    ____             __  __
   / __ )___  _____/ /_/ /_
  / __  / _ \/ ___/ __/ __ \
 / /_/ /  __/ /  / /_/ / / /
/_____/\___/_/   \__/_/ /_/
        WordPress and PHP hosting for Proxmox
EOF
  echo
}

command -v pveversion >/dev/null || die "Run this on a Proxmox VE host."
[[ $EUID -eq 0 ]] || die "Run as root."
header

ask() { # ask VAR "Question" default
  local var="$1" q="$2" def="$3" val
  if [[ -n "${!var:-}" ]]; then return; fi
  if [[ "${UNATTENDED:-0}" == "1" ]] || ! command -v whiptail >/dev/null; then
    printf -v "$var" '%s' "$def"; return
  fi
  val=$(whiptail --backtitle "Berth" --title "$APP LXC" --inputbox "$q" 9 64 "$def" 3>&1 1>&2 2>&3) || die "Cancelled"
  printf -v "$var" '%s' "$val"
}

ask_password() { # ask_password VAR "Question"
  local var="$1" q="$2" a b
  if [[ -n "${!var+x}" ]]; then return; fi
  if [[ "${UNATTENDED:-0}" == "1" ]] || ! command -v whiptail >/dev/null; then
    printf -v "$var" '%s' ""; return
  fi
  while true; do
    a=$(whiptail --backtitle "Berth" --title "$APP LXC" --passwordbox "$q" 9 64 3>&1 1>&2 2>&3) || die "Cancelled"
    [[ -z "$a" ]] && break
    b=$(whiptail --backtitle "Berth" --title "$APP LXC" --passwordbox "Repeat the root password" 9 64 3>&1 1>&2 2>&3) || die "Cancelled"
    [[ "$a" == "$b" ]] && break
    whiptail --backtitle "Berth" --title "$APP LXC" --msgbox "Passwords did not match." 8 40
  done
  printf -v "$var" '%s' "$a"
}

ask CTID "Container ID" "$(pvesh get /cluster/nextid)"
# Not $HOSTNAME: bash sets that to the Proxmox node name.
ask CT_HOSTNAME "Hostname" "berth"
ask CORE_COUNT "CPU cores" "2"
ask RAM_SIZE "Memory (MiB)" "2048"
ask DISK_SIZE "Disk size (GiB)" "20"
ask BRIDGE "Bridge" "vmbr0"
ask NET "IPv4 (CIDR like 10.0.10.20/24, or dhcp). A fixed address is best: DNS points at it." "dhcp"
if [[ "$NET" != "dhcp" ]]; then ask GATE "Gateway" ""; fi
ask BERTH_PHP "PHP version to install first" "8.4"
ask BACKUP_MOUNT "Folder on this Proxmox host for backups (blank: keep them inside the container)" ""
ask_password ROOT_PASSWORD "Root password for the container (blank: no password, console logs in automatically)"
DEFAULT_KEYS=""
[[ -s /root/.ssh/authorized_keys ]] && DEFAULT_KEYS="/root/.ssh/authorized_keys"
ask SSH_KEY_FILE "SSH public keys for root (file path, blank for none)" "$DEFAULT_KEYS"
ask STORAGE "Storage for the root disk" "$(pvesm status -content rootdir | awk 'NR==2{print $1}')"
ask TEMPLATE_STORAGE "Storage for templates" "$(pvesm status -content vztmpl | awk 'NR==2{print $1}')"

[[ "$CTID" =~ ^[0-9]+$ ]] || die "Invalid container ID"
pct status "$CTID" >/dev/null 2>&1 && die "Container $CTID already exists"
[[ -n "$STORAGE" && -n "$TEMPLATE_STORAGE" ]] || die "No suitable storage found"
[[ -z "${SSH_KEY_FILE:-}" || -r "$SSH_KEY_FILE" ]] || die "SSH key file $SSH_KEY_FILE not found"
[[ -z "${BACKUP_MOUNT:-}" || -d "$BACKUP_MOUNT" ]] || die "Backup folder $BACKUP_MOUNT does not exist on this host"
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
NETS=(--net0 "$NET0")
MOUNTS=()
if [[ -n "${BACKUP_MOUNT:-}" ]]; then
  # Unprivileged containers write as uid 100000 on the host, so that user
  # needs write access to the folder.
  MOUNTS=(--mp0 "${BACKUP_MOUNT},mp=/var/backups/berth")
fi

msg_info "Creating LXC $CTID"
pct create "$CTID" "${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}" \
  --hostname "$CT_HOSTNAME" --cores "$CORE_COUNT" --memory "$RAM_SIZE" --swap 512 \
  --rootfs "${STORAGE}:${DISK_SIZE}" "${NETS[@]}" "${MOUNTS[@]}" "${ACCESS[@]}" \
  --unprivileged 1 --features nesting=1 --onboot 1 --tags berth \
  --description "Berth hosting panel (nginx, PHP-FPM, MariaDB). https://github.com/${REPO}" >/dev/null
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
  msg_ok "Console logs in as root automatically"
fi

# Locate the installer: next to this script when run from a clone, else GitHub.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo /nonexistent)"
INSTALLER="$(mktemp)"
trap 'rm -f "$INSTALLER"' EXIT
if [[ -f "$SCRIPT_DIR/../install/berth-install.sh" ]]; then
  cp "$SCRIPT_DIR/../install/berth-install.sh" "$INSTALLER"
else
  curl -fsSL "https://raw.githubusercontent.com/${REPO}/${BRANCH}/install/berth-install.sh" -o "$INSTALLER" \
    || die "Could not download the installer"
fi
pct push "$CTID" "$INSTALLER" /root/berth-install.sh --perms 0755
# C.UTF-8 always exists; the host shell's locale often is not installed in the CT.
ENVS=("LANG=C.UTF-8" "LC_ALL=C.UTF-8" "BERTH_REPO=${REPO}" "BERTH_PHP=${BERTH_PHP}")
[[ -n "${BERTH_VERSION:-}" ]] && ENVS+=("BERTH_VERSION=${BERTH_VERSION}")
if [[ -n "${BERTH_BINARY:-}" ]]; then
  pct push "$CTID" "$BERTH_BINARY" /root/berth --perms 0755
  ENVS+=("BERTH_BINARY=/root/berth")
fi

msg_info "Installing Berth inside the container (this takes a few minutes)"
echo
pct exec "$CTID" -- env "${ENVS[@]}" bash /root/berth-install.sh
pct exec "$CTID" -- rm -f /root/berth-install.sh /root/berth

IP="$(pct exec "$CTID" -- ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1 | head -1)"
echo
echo -e " ${GN}${BOLD}Berth is installed in LXC ${CTID}.${CL}"
echo -e "   Web UI: ${BL}https://${IP}:8443${CL} (create the admin account straight away)"
echo -e "   Sites:  ports 80 and 443 on ${BL}${IP}${CL} (forward them from your public address)"
echo
if [[ -n "${ROOT_PASSWORD:-}" ]]; then
  echo -e "   Shell: Proxmox console or ${BL}pct enter ${CTID}${CL}, root with the password you set"
else
  echo -e "   Shell: Proxmox console (logs in automatically) or ${BL}pct enter ${CTID}${CL}"
fi
[[ -n "${SSH_KEY_FILE:-}" ]] && echo -e "   SSH: ${BL}ssh root@${IP}${CL} with your key"
echo -e "   Update later: ${BL}pct exec ${CTID} -- berth-update${CL} (or run berth-update inside the CT)"
[[ -n "${BACKUP_MOUNT:-}" ]] && echo -e "   Backups are kept on this host in ${BL}${BACKUP_MOUNT}${CL}"
echo
