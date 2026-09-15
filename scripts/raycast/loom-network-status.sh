#!/bin/zsh

# @raycast.schemaVersion 1
# @raycast.title LOOM Network Status
# @raycast.mode fullOutput
# @raycast.packageName LOOM
# @raycast.icon assets/loom-network.png
# @raycast.description Show WireGuard plus exact Main Storage and Main Box SMB mount identities.

set -euo pipefail
export LC_ALL=C
export LANG=C
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

MAC_WG_IP="${LOOM_MACBOOK_WG_IP:-10.44.0.5}"
MAIN_HOST="${LOOM_MAIN_SMB_HOST:-loom-storage}"
MAIN_EXPECTED_IP="${LOOM_MAIN_SMB_IP:-${LOOM_MAIN_STORAGE_HOST:-10.44.0.2}}"
MAIN_USER="${LOOM_MAIN_SMB_USER:-loomshare}"
MAIN_STORAGE_MOUNT="${LOOM_MAIN_STORAGE_MOUNT:-$HOME/loom-storage}"
MAIN_SHARE="${LOOM_MAIN_SMB_SHARE:-loom-storage}"
MAIN_FINDER_MOUNT="${LOOM_MAIN_SMB_FINDER_MOUNT:-/Volumes/${MAIN_SHARE}}"
MAIN_URL="smb://${MAIN_USER}@${MAIN_HOST}/${MAIN_SHARE}"
BOX_HOST="${LOOM_MAIN_BOX_SMB_HOST:-$MAIN_HOST}"
BOX_EXPECTED_IP="${LOOM_MAIN_BOX_SMB_IP:-$MAIN_EXPECTED_IP}"
BOX_USER="${LOOM_MAIN_BOX_SMB_USER:-$MAIN_USER}"
BOX_MOUNT="${LOOM_MAIN_BOX_MOUNT:-$HOME/loom-main-box}"
BOX_SHARE="${LOOM_MAIN_BOX_SMB_SHARE:-loom-main-box}"
BOX_FINDER_MOUNT="${LOOM_MAIN_BOX_SMB_FINDER_MOUNT:-/Volumes/${BOX_SHARE}}"
BOX_URL="smb://${BOX_USER}@${BOX_HOST}/${BOX_SHARE}"
WATCHDOG_LABEL="local.loom.network-watchdog"
SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"

has_wg_address() {
  ifconfig 2>/dev/null | grep -q "inet ${MAC_WG_IP} "
}

has_private_route() {
  netstat -rn -f inet 2>/dev/null | awk '{print $1}' | grep -Eq '^(10\.44|10\.44/24|10\.44\.0\.0/24)$'
}

resolve_host_ip() {
  local host="$1"
  if [[ "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf '%s\n' "$host"
    return
  fi
  if command -v dscacheutil >/dev/null 2>&1; then
    dscacheutil -q host -a name "$host" 2>/dev/null | awk '/ip_address:/ { print $2; exit }'
    return
  fi
  awk -v host="$host" '
    $1 !~ /^#/ {
      for (i = 2; i <= NF; i++) {
        if ($i == host) {
          print $1
          exit
        }
      }
    }
  ' /etc/hosts 2>/dev/null || true
}

tcp_reachable() {
  command -v nc >/dev/null 2>&1 && nc -G 2 -z "$1" 445 >/dev/null 2>&1
}

mount_source_at() {
  local wanted="$1" line source
  while IFS= read -r line; do
    case "$line" in
      *" on $wanted ("*)
        source="${line%% on *}"
        printf '%s\n' "$source"
        return 0
        ;;
    esac
  done < <(mount 2>/dev/null)
  return 1
}

smb_source_matches() {
  local source="$1" user="$2" host="$3" expected_ip="$4" share="$5"
  case "$source" in
    "//$user@$host/$share"|"//$host/$share"|"//$user@$expected_ip/$share"|"//$expected_ip/$share") return 0 ;;
    *) return 1 ;;
  esac
}

expected_mount_path() {
  local path="$1" user="$2" host="$3" expected_ip="$4" share="$5" source
  source="$(mount_source_at "$path" 2>/dev/null || true)"
  [[ -n "$source" ]] && smb_source_matches "$source" "$user" "$host" "$expected_ip" "$share"
}

print_mount_identity() {
  local label="$1" path="$2" finder_path="$3" user="$4" host="$5" expected_ip="$6" share="$7"
  local candidate source found=0 seen=""
  printf '%s mount: ' "$label"
  for candidate in "$path" "$finder_path"; do
    [[ -n "$candidate" ]] || continue
    case " $seen " in *" $candidate "*) continue ;; esac
    seen="$seen $candidate"
    source="$(mount_source_at "$candidate" 2>/dev/null || true)"
    [[ -n "$source" ]] || continue
    if (( found == 0 )); then
      printf 'mounted\n'
    fi
    found=1
    if smb_source_matches "$source" "$user" "$host" "$expected_ip" "$share"; then
      printf '  - %s <- %s\n' "$candidate" "$source"
    else
      printf '  - CONFLICT: %s <- %s (expected //%s@%s/%s)\n' "$candidate" "$source" "$user" "$host" "$share"
    fi
  done
  if (( found == 0 )); then
    printf 'not mounted\n'
  fi
}

printf 'LOOM Network Status\n\n'
printf 'state: '
launchctl print "system/$WATCHDOG_LABEL" >/dev/null 2>&1 && printf 'loom-up\n' || printf 'loom-down\n'

if has_wg_address && has_private_route; then
  printf 'network: UP\n'
else
  printf 'network: DOWN\n'
fi
printf 'wireguard address: '
has_wg_address && printf '%s\n' "$MAC_WG_IP" || printf 'missing\n'
printf 'wireguard route: '
has_private_route && printf '10.44/24 present\n' || printf 'missing\n'

for identity in storage box; do
  if [[ "$identity" == storage ]]; then
    host="$MAIN_HOST"; expected="$MAIN_EXPECTED_IP"; share="$MAIN_SHARE"
  else
    host="$BOX_HOST"; expected="$BOX_EXPECTED_IP"; share="$BOX_SHARE"
  fi
  resolved_ip="$(resolve_host_ip "$host" || true)"
  printf '%s host: ' "$identity"
  if [[ -z "$resolved_ip" ]]; then
    printf '%s unresolved\n' "$host"
  elif [[ "$resolved_ip" == "$expected" ]]; then
    printf '%s -> %s\n' "$host" "$resolved_ip"
  else
    printf '%s -> %s (expected %s)\n' "$host" "$resolved_ip" "$expected"
  fi
  printf '%s SMB: ' "$identity"
  tcp_reachable "$host" && printf 'reachable (%s:445, share %s)\n' "$host" "$share" || printf 'unreachable (%s:445, share %s)\n' "$host" "$share"
done

print_mount_identity "storage (read-only)" "$MAIN_STORAGE_MOUNT" "$MAIN_FINDER_MOUNT" "$MAIN_USER" "$MAIN_HOST" "$MAIN_EXPECTED_IP" "$MAIN_SHARE"
print_mount_identity "main Box (read/write)" "$BOX_MOUNT" "$BOX_FINDER_MOUNT" "$BOX_USER" "$BOX_HOST" "$BOX_EXPECTED_IP" "$BOX_SHARE"
printf 'watchdog: '
launchctl print "system/$WATCHDOG_LABEL" >/dev/null 2>&1 && printf 'ON\n' || printf 'OFF\n'

printf '\n'
if has_wg_address && has_private_route && tcp_reachable "$MAIN_HOST"; then
  if expected_mount_path "$MAIN_STORAGE_MOUNT" "$MAIN_USER" "$MAIN_HOST" "$MAIN_EXPECTED_IP" "$MAIN_SHARE" || expected_mount_path "$MAIN_FINDER_MOUNT" "$MAIN_USER" "$MAIN_HOST" "$MAIN_EXPECTED_IP" "$MAIN_SHARE"; then
    printf 'Ready: LOOM private network and the read-only Storage mount are usable.\n'
  else
    printf 'Action: open %s in Finder to mount read-only LOOM Main Storage.\n' "$MAIN_URL"
  fi
  if ! expected_mount_path "$BOX_MOUNT" "$BOX_USER" "$BOX_HOST" "$BOX_EXPECTED_IP" "$BOX_SHARE" && ! expected_mount_path "$BOX_FINDER_MOUNT" "$BOX_USER" "$BOX_HOST" "$BOX_EXPECTED_IP" "$BOX_SHARE"; then
    printf 'Optional: open %s in Finder to mount the writable Main Box.\n' "$BOX_URL"
  fi
else
  printf 'Action: run "LOOM Network Up". If SMB stays unreachable, check the private Samba binding on main.\n'
fi

printf '\nNode-agent storage policy:\n'
"$LOOM_MACBOOK" storage-status 2>/dev/null || printf 'unavailable; run scripts/loom-macbook storage-status for details\n'
printf '\nLOOM Cloud mount:\n'
"$LOOM_MACBOOK" cloud-status 2>/dev/null || printf 'unavailable; run scripts/loom-macbook cloud-status for details\n'
