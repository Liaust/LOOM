#!/usr/bin/env bash
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

ORCA_BIN="${ORCA_BIN:-/Applications/Orca.app/Contents/Resources/bin/orca}"
ORCA_APP_PLIST="${ORCA_APP_PLIST:-/Applications/Orca.app/Contents/Info.plist}"
ORCA_EXPECTED_VERSION="${ORCA_EXPECTED_VERSION:-1.4.183}"
ORCA_EXPECTED_SHA256="${ORCA_EXPECTED_SHA256:-df238edc6169a439a6310c83937f556303a20370dd6723230d82a73e1a6110f0}"
ORCA_TEST_PORT="${ORCA_TEST_PORT:-17668}"
CADDY_TEST_PORT="${CADDY_TEST_PORT:-17669}"
MAC_WG_IP="${MAC_WG_IP:-10.44.0.5}"
VPS_WG_IP="${VPS_WG_IP:-10.44.0.1}"
IPHONE_WG_IP="${IPHONE_WG_IP:-10.44.0.6}"
VPS_PUBLIC_ENDPOINT="${VPS_PUBLIC_ENDPOINT:-192.0.2.1:51820}"
VPS_HOST="${VPS_HOST:-loom-vps}"
MOBILE_ASSISTED="${ORCA_ENABLE_TEMP_IPHONE_PEER:-0}"
PREFLIGHT_ONLY="${ORCA_NETWORK_PREFLIGHT_ONLY:-0}"
LONG_OPERATION_SECONDS="${ORCA_NETWORK_LONG_OPERATION_SECONDS:-5}"

PROOF_ROOT=""
ORCA_PROFILE=""
ORCA_SERVER_PID=""
CADDY_PID=""
DISPOSABLE_REPO_ID=""
DISPOSABLE_PROJECT_ID=""
DISPOSABLE_WORKTREE_ID=""
DISPOSABLE_WORKTREE_PATH=""
IPHONE_PUBLIC_KEY=""
CLEANUP_FAILURES=0

fail() {
  printf 'v2-orca-pre-main-network: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

orca_profile() {
  ORCA_USER_DATA_PATH="$ORCA_PROFILE" "$ORCA_BIN" "$@"
}

port_is_listening() {
  lsof -nP -iTCP:"$1" -sTCP:LISTEN 2>/dev/null | sed -n '2p' | grep -q .
}

vps_iphone_peer_count() {
  ssh -o BatchMode=yes -o ConnectTimeout=8 "$VPS_HOST" 'sudo wg show wg0 allowed-ips' \
    | awk '{print $2}' \
    | grep -Ec "(^|,)${IPHONE_WG_IP//./\\.}/32(,|$)" \
    || true
}

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e

  if [[ -n "$DISPOSABLE_WORKTREE_ID" && -n "$ORCA_PROFILE" && -n "$ORCA_SERVER_PID" ]]; then
    orca_profile terminal stop --worktree "id:$DISPOSABLE_WORKTREE_ID" --json >/dev/null 2>&1 || true
    orca_profile worktree rm --worktree "id:$DISPOSABLE_WORKTREE_ID" --force --json >/dev/null 2>&1 || CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
  fi

  if [[ -n "$DISPOSABLE_REPO_ID" && -n "$ORCA_PROFILE" && -n "$ORCA_SERVER_PID" ]]; then
    orca_profile project setup-delete --setup "$DISPOSABLE_REPO_ID" --json >/dev/null 2>&1 || CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
  fi

  if [[ -n "$CADDY_PID" ]]; then
    kill "$CADDY_PID" >/dev/null 2>&1 || true
    wait "$CADDY_PID" >/dev/null 2>&1 || true
  fi

  if [[ -n "$ORCA_SERVER_PID" ]]; then
    kill "$ORCA_SERVER_PID" >/dev/null 2>&1 || true
    wait "$ORCA_SERVER_PID" >/dev/null 2>&1 || true
  fi

  if [[ -n "$IPHONE_PUBLIC_KEY" ]]; then
    ssh -o BatchMode=yes -o ConnectTimeout=8 "$VPS_HOST" \
      "sudo wg set wg0 peer '$IPHONE_PUBLIC_KEY' remove" >/dev/null 2>&1 || CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
    if [[ "$(vps_iphone_peer_count)" != "0" ]]; then
      CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
    fi
  fi

  if [[ -n "$PROOF_ROOT" && -e "$PROOF_ROOT" ]]; then
    case "$(realpath "$PROOF_ROOT")" in
      /private/tmp/loom-orca-network.*)
        find "$PROOF_ROOT" -xdev -depth -delete || CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
        ;;
      *)
        printf 'refusing unexpected cleanup target: %s\n' "$PROOF_ROOT" >&2
        CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
        ;;
    esac
  fi

  if port_is_listening "$ORCA_TEST_PORT" || port_is_listening "$CADDY_TEST_PORT"; then
    CLEANUP_FAILURES=$((CLEANUP_FAILURES + 1))
  fi

  if ((CLEANUP_FAILURES > 0)); then
    printf 'cleanup_failures=%d\n' "$CLEANUP_FAILURES" >&2
    exit 1
  fi
  exit "$exit_status"
}

trap cleanup EXIT INT TERM HUP

check_versions() {
  local app_version executable_sha
  app_version="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$ORCA_APP_PLIST")"
  executable_sha="$(shasum -a 256 "$ORCA_BIN" | awk '{print $1}')"
  [[ "$app_version" == "$ORCA_EXPECTED_VERSION" ]] || fail "Orca app is $app_version, expected $ORCA_EXPECTED_VERSION"
  [[ "$executable_sha" == "$ORCA_EXPECTED_SHA256" ]] || fail "Orca wrapper hash does not match the pinned release"
  printf 'orca_version=%s\n' "$app_version"
  printf 'orca_wrapper_sha256=%s\n' "$executable_sha"
  printf 'wireguard_version=%s\n' "$(wg --version | sed -n '1p')"
  printf 'caddy_version=%s\n' "$(caddy version | awk '{print $1}')"
  printf 'websocat_version=%s\n' "$(websocat --version | awk '{print $2}')"
  printf 'node_version=%s\n' "$(node --version)"
  printf 'jq_version=%s\n' "$(jq --version)"
}

check_wireguard_route() {
  local wg_interface
  wg_interface="$(ifconfig | awk -v ip="$MAC_WG_IP" '/^[a-z0-9]+:/{iface=$1} $0 ~ "inet " ip " " {sub(/:$/, "", iface); print iface; exit}')"
  [[ -n "$wg_interface" ]] || fail "Mac WireGuard address $MAC_WG_IP is absent"
  netstat -rn -f inet | awk -v iface="$wg_interface" '$1 == "10.44/24" && $4 == iface {found=1} END {exit(found ? 0 : 1)}' \
    || fail "narrow 10.44/24 route is absent from $wg_interface"
  if netstat -rn -f inet | awk -v iface="$wg_interface" '$1 == "default" && $4 == iface {found=1} END {exit(found ? 0 : 1)}'; then
    fail "default route unexpectedly uses $wg_interface"
  fi
  printf 'wireguard_interface=%s\n' "$wg_interface"
  printf 'wireguard_address=%s\n' "$MAC_WG_IP"
  printf 'wireguard_route=10.44.0.0/24\n'
  printf 'default_route_through_wireguard=false\n'
}

check_vps() {
  ping -c 2 -W 1000 "$VPS_WG_IP" >/dev/null || fail "VPS WireGuard address is unreachable"
  ssh -o BatchMode=yes -o ConnectTimeout=8 "$VPS_HOST" \
    'test "$(sudo wg show interfaces)" = wg0 && test "$(sudo wg show wg0 listen-port)" = 51820' \
    || fail "VPS WireGuard interface/listen-port check failed"
  [[ "$(vps_iphone_peer_count)" == "0" ]] || fail "$IPHONE_WG_IP/32 is already allocated; update the plan before choosing another address"
  printf 'vps_reachable=true\n'
  printf 'vps_wireguard_port=51820\n'
  printf 'iphone_peer_present_before=false\n'
  printf 'main_probe=skipped_by_task_boundary\n'
}

check_default_host_stopped() {
  local default_status runtime_state graph_state
  default_status="$("$ORCA_BIN" status --json)"
  runtime_state="$(jq -r '.result.runtime.state // "unknown"' <<<"$default_status")"
  graph_state="$(jq -r '.result.graph.state // "unknown"' <<<"$default_status")"
  if [[ "$runtime_state" != "not_running" || "$graph_state" != "not_running" ]]; then
    fail "the operator must finish/park active Orca work, quit the normal Orca host, and rerun; observed runtime=$runtime_state graph=$graph_state"
  fi
  printf 'default_orca_host=stopped\n'
}

redact_ready_json() {
  jq -Rrc '
    fromjson?
    | select(.type == "orca_server_ready")
    | {
        type,
        schemaVersion,
        boundEndpoint,
        advertisedEndpoint,
        managedWslCliReconciliation,
        pairing: {
          available: (.pairing.available // false),
          reason: (.pairing.reason // null),
          guidance: (.pairing.guidance // null),
          endpoint: (.pairing.endpoint // null),
          scope: (.pairing.scope // null)
        }
      }
  '
}

wait_for_ready() {
  local attempt
  for attempt in $(seq 1 60); do
    if redact_ready_json <"$PROOF_ROOT/orca-serve.raw" >"$PROOF_ROOT/orca-serve.redacted" && \
      jq -e 'select(.type == "orca_server_ready" and .schemaVersion == 1)' "$PROOF_ROOT/orca-serve.redacted" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "$ORCA_SERVER_PID" >/dev/null 2>&1; then
      sed -n '1,80p' "$PROOF_ROOT/orca-serve.stderr" >&2
      fail "Orca server exited before readiness"
    fi
    sleep 0.5
  done
  fail "Orca readiness JSON did not arrive within 30 seconds"
}

websocket_upgrade() {
  local host_name=$1 port_number=$2 headers_file=$3 curl_status=0
  curl --http1.1 --silent --show-error --max-time 3 \
    --dump-header "$headers_file" --output /dev/null \
    --header 'Connection: Upgrade' \
    --header 'Upgrade: websocket' \
    --header 'Sec-WebSocket-Version: 13' \
    --header 'Sec-WebSocket-Key: bG9vbS1vcmNhLXByb29mLQ==' \
    "http://$host_name:$port_number/" || curl_status=$?
  [[ "$curl_status" == 0 || "$curl_status" == 28 ]] || fail "WebSocket request failed for $host_name:$port_number (curl=$curl_status)"
  grep -Eq '^HTTP/[0-9.]+ 101([[:space:]]|$)' "$headers_file" || fail "WebSocket upgrade did not return 101 for $host_name:$port_number"
}

start_caddy() {
  {
    printf '{\n  admin off\n  auto_https off\n}\n'
    printf 'http://127.0.0.1:%s {\n  reverse_proxy 127.0.0.1:%s\n}\n' "$CADDY_TEST_PORT" "$ORCA_TEST_PORT"
  } >"$PROOF_ROOT/Caddyfile"
  caddy run --config "$PROOF_ROOT/Caddyfile" --adapter caddyfile \
    >"$PROOF_ROOT/caddy.stdout" 2>"$PROOF_ROOT/caddy.stderr" &
  CADDY_PID=$!
  local attempt
  for attempt in $(seq 1 30); do
    port_is_listening "$CADDY_TEST_PORT" && return 0
    kill -0 "$CADDY_PID" >/dev/null 2>&1 || fail "disposable Caddy exited before listening"
    sleep 0.2
  done
  fail "disposable Caddy did not listen on loopback"
}

create_network_fixture() {
  local fixture_source nonce_file nonce_value create_json terminal_json
  fixture_source="$PROOF_ROOT/research-notes-network-proof"
  mkdir -p "$fixture_source"
  git -C "$REPO_ROOT" archive HEAD:examples/projects/research-notes | tar -x -C "$fixture_source"
  git -C "$fixture_source" init -b main >/dev/null
  git -C "$fixture_source" config user.name 'ORCA Network Proof'
  git -C "$fixture_source" config user.email 'orca-network-proof@invalid.example'
  git -C "$fixture_source" add .
  git -C "$fixture_source" commit -m 'fixture: canonical loom network proof' >/dev/null

  DISPOSABLE_REPO_ID="$(orca_profile repo add --path "$fixture_source" --json | jq -er '.result.repo.id')"
  DISPOSABLE_PROJECT_ID="repo:$DISPOSABLE_REPO_ID"
  create_json="$(orca_profile worktree create --repo "id:$DISPOSABLE_REPO_ID" --name orca-network-proof --no-parent --base-branch main --setup skip --json)"
  DISPOSABLE_WORKTREE_ID="$(jq -er '.result.worktree.id' <<<"$create_json")"
  DISPOSABLE_WORKTREE_PATH="$(jq -er '.result.worktree.path' <<<"$create_json")"

  nonce_value="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  nonce_file="$DISPOSABLE_WORKTREE_PATH/notes/NETWORK_NONCE.txt"
  printf '%s\n' "$nonce_value" >"$nonce_file"
  terminal_json="$(orca_profile terminal create --worktree "id:$DISPOSABLE_WORKTREE_ID" --title network-operation --command "/bin/sleep $LONG_OPERATION_SECONDS" --json)"
  jq -e '.ok == true and (.result.terminal.handle | length > 0)' <<<"$terminal_json" >/dev/null

  [[ "$(git -C "$fixture_source" status --porcelain | wc -l | tr -d ' ')" == "0" ]] || fail "disposable source repository is dirty"
  [[ "$(git -C "$DISPOSABLE_WORKTREE_PATH" status --porcelain | wc -l | tr -d ' ')" == "1" ]] || fail "network worktree does not have exactly one nonce change"
  printf 'network_fixture_repo_id_present=true\n'
  printf 'network_fixture_worktree_id_present=true\n'
  printf 'network_fixture_branch=%s\n' "$(git -C "$DISPOSABLE_WORKTREE_PATH" branch --show-current)"
  printf 'network_fixture_uncommitted_paths=1\n'
  printf 'network_operation_seconds=%s\n' "$LONG_OPERATION_SECONDS"
}

add_temporary_iphone_peer() {
  [[ -t 0 ]] || fail "temporary iPhone peer mode requires an interactive operator terminal"
  ((LONG_OPERATION_SECONDS >= 900)) || fail "mobile-assisted mode requires ORCA_NETWORK_LONG_OPERATION_SECONDS>=900"

  local iphone_private_file iphone_public_file iphone_config_file vps_public_key
  iphone_private_file="$PROOF_ROOT/iphone.private"
  iphone_public_file="$PROOF_ROOT/iphone.public"
  iphone_config_file="$PROOF_ROOT/iphone-wireguard.conf"
  umask 077
  wg genkey >"$iphone_private_file"
  wg pubkey <"$iphone_private_file" >"$iphone_public_file"
  IPHONE_PUBLIC_KEY="$(tr -d '\n' <"$iphone_public_file")"
  vps_public_key="$(ssh -o BatchMode=yes -o ConnectTimeout=8 "$VPS_HOST" 'sudo wg show wg0 public-key')"

  {
    printf '[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n' "$(tr -d '\n' <"$iphone_private_file")" "$IPHONE_WG_IP"
    printf '[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 10.44.0.0/24\nPersistentKeepalive = 25\n' "$vps_public_key" "$VPS_PUBLIC_ENDPOINT"
  } >"$iphone_config_file"
  chmod 600 "$iphone_private_file" "$iphone_public_file" "$iphone_config_file"
  grep -F 'AllowedIPs = 10.44.0.0/24' "$iphone_config_file" >/dev/null
  if grep -F '0.0.0.0/0' "$iphone_config_file" >/dev/null; then
    fail "temporary iPhone config contains a default route"
  fi

  printf 'temporary_peer_public_key=%s\n' "$IPHONE_PUBLIC_KEY"
  printf 'temporary_peer_address=%s/32\n' "$IPHONE_WG_IP"
  printf "temporary_peer_removal=ssh %q sudo wg set wg0 peer %q remove\n" "$VPS_HOST" "$IPHONE_PUBLIC_KEY"
  printf 'temporary_peer_cleanup_trap=installed\n'
  ssh -o BatchMode=yes -o ConnectTimeout=8 "$VPS_HOST" \
    "sudo wg set wg0 peer '$IPHONE_PUBLIC_KEY' allowed-ips '$IPHONE_WG_IP/32'"
  [[ "$(vps_iphone_peer_count)" == "1" ]] || fail "temporary iPhone peer was not installed exactly once"

  printf 'operator_only_wireguard_config=%s\n' "$iphone_config_file"
  printf 'operator_only_pairing_json=%s\n' "$PROOF_ROOT/orca-serve.raw"
  printf 'Import both temporary credentials without copying them into chat or logs. Press Return only after import/pairing is complete.\n'
  read -r
}

require_command curl
require_command caddy
require_command git
require_command ifconfig
require_command jq
require_command lsof
require_command node
require_command ping
require_command realpath
require_command shasum
require_command ssh
require_command tar
require_command uuidgen
require_command websocat
require_command wg

check_versions
check_wireguard_route
check_vps

if [[ "$PREFLIGHT_ONLY" == "1" ]]; then
  printf 'preflight_only=true\n'
  exit 0
fi

check_default_host_stopped
port_is_listening "$ORCA_TEST_PORT" && fail "Orca test port $ORCA_TEST_PORT is already in use"
port_is_listening "$CADDY_TEST_PORT" && fail "Caddy test port $CADDY_TEST_PORT is already in use"

PROOF_ROOT="$(mktemp -d /tmp/loom-orca-network.XXXXXX)"
chmod 700 "$PROOF_ROOT"
ORCA_PROFILE="$PROOF_ROOT/profile"
mkdir -p "$ORCA_PROFILE"
chmod 700 "$ORCA_PROFILE"

serve_args=(serve --port "$ORCA_TEST_PORT" --pairing-address "$MAC_WG_IP" --json)
if [[ "$MOBILE_ASSISTED" == "1" ]]; then
  serve_args+=(--mobile-pairing)
else
  serve_args+=(--no-pairing)
fi

ORCA_USER_DATA_PATH="$ORCA_PROFILE" DO_NOT_TRACK=1 ORCA_TELEMETRY_DISABLED=1 \
  "$ORCA_BIN" "${serve_args[@]}" >"$PROOF_ROOT/orca-serve.raw" 2>"$PROOF_ROOT/orca-serve.stderr" &
ORCA_SERVER_PID=$!
wait_for_ready

jq -e --arg bound_port ":$ORCA_TEST_PORT" --arg advertised "ws://$MAC_WG_IP:$ORCA_TEST_PORT" '
  .type == "orca_server_ready"
  and .schemaVersion == 1
  and (.boundEndpoint | endswith($bound_port))
  and .advertisedEndpoint == $advertised
' "$PROOF_ROOT/orca-serve.redacted" >/dev/null
printf 'orca_ready_schema=1\n'
printf 'orca_advertised_endpoint=ws://%s:%s\n' "$MAC_WG_IP" "$ORCA_TEST_PORT"
printf 'pairing_available=%s\n' "$(jq -r '.pairing.available' "$PROOF_ROOT/orca-serve.redacted")"

websocket_upgrade 127.0.0.1 "$ORCA_TEST_PORT" "$PROOF_ROOT/orca-loopback.headers"
websocket_upgrade "$MAC_WG_IP" "$ORCA_TEST_PORT" "$PROOF_ROOT/orca-wireguard.headers"
printf 'direct_websocket_loopback=true\n'
printf 'direct_websocket_wireguard=true\n'

start_caddy
websocket_upgrade 127.0.0.1 "$CADDY_TEST_PORT" "$PROOF_ROOT/caddy-websocket.headers"
printf 'caddy_listener=127.0.0.1:%s\n' "$CADDY_TEST_PORT"
printf 'caddy_websocket_upgrade=true\n'
printf 'caddy_tls_claim=false\n'

create_network_fixture

if [[ "$MOBILE_ASSISTED" == "1" ]]; then
  add_temporary_iphone_peer
  printf 'Complete the runbook transition matrix, then press Return to remove the peer, profile, worktree, repo, listener, and config.\n'
  read -r
else
  printf 'mobile_assisted=false\n'
fi

printf 'PASS: v2 Orca pre-main network harness\n'
