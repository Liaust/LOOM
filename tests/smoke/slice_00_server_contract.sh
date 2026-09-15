#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"

pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

remote() {
  ssh -o BatchMode=yes "$HOST" "$@"
}

check_remote() {
  local label="$1"
  local command="$2"
  remote "$command" >/dev/null
  pass "$label"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"

hostname="$(remote 'hostname')"
[[ "$hostname" == "loom-dev" ]] || fail "expected hostname loom-dev, got $hostname"
pass "SSH target reachable"

user="$(remote 'whoami')"
[[ "$user" == "loomadmin" ]] || fail "expected SSH user loomadmin, got $user"
pass "SSH user is loomadmin"

root_source="$(remote 'findmnt -n -o SOURCE /')"
[[ "$root_source" != "tmpfs" ]] || fail "root filesystem is tmpfs; VM is not booted from installed disk"
pass "root filesystem is persistent"

check_remote "PostgreSQL active" 'systemctl is-active postgresql'
check_remote "PostgreSQL setup active" 'systemctl is-active postgresql-setup'
check_remote "loomd active" 'systemctl is-active loomd'
check_remote "loom user exists" 'id loom'
check_remote "loom group exists" 'getent group loom'

for path in \
  /var/lib/loom \
  /var/lib/loom/object-store \
  /var/lib/loom/object-store/blobs \
  /var/lib/loom/object-store/objects \
  /var/lib/loom/object-store/artifacts \
  /var/lib/loom/indexes \
  /var/lib/loom/packages \
  /var/lib/loom/state \
  /var/lib/loom/temp \
  /srv/loom/current \
  /etc/loom
do
  check_remote "directory exists: $path" "sudo test -d '$path'"
done

check_remote "object store writable by loom" 'sudo -u loom test -w /var/lib/loom/object-store'
check_remote "temp dir writable by loom" 'sudo -u loom sh -c "touch /var/lib/loom/temp/smoke-write-test && rm /var/lib/loom/temp/smoke-write-test"'
check_remote "source dir writable by deploy user" "test -w '$REMOTE_DIR'"
check_remote "loom can read source dir" "sudo -u loom test -r '$REMOTE_DIR' && sudo -u loom test -x '$REMOTE_DIR'"
check_remote "loom can read config dir" 'sudo -u loom test -r /etc/loom'

db_probe="$(remote "sudo -u loom psql -d loom_main -tAc \"SELECT current_user || ':' || current_database();\"")"
[[ "$db_probe" == "loom:loom_main" ]] || fail "unexpected database probe: $db_probe"
pass "loom can connect to loom_main"

db_owner="$(remote "sudo -u postgres psql -tAc \"SELECT pg_catalog.pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'loom_main';\"")"
[[ "$db_owner" == "loom" ]] || fail "expected loom_main owner loom, got $db_owner"
pass "loom owns loom_main"

check_remote "PostgreSQL listens locally only" "! ss -ltn | awk '{print \$4}' | grep -qE '^(0\\.0\\.0\\.0:5432|\\[::\\]:5432|\\*:5432)$'"
check_remote "loomd logs visible" 'journalctl -u loomd -n 1 --no-pager'
check_remote "repo synced to source dir" "test -f '$REMOTE_DIR/flake.nix' && test -d '$REMOTE_DIR/nix'"

if grep -R "adapters/utm" "$REPO_ROOT/nix/modules" >/dev/null 2>&1; then
  fail "reusable nix/modules imports or references adapters/utm"
fi
pass "reusable modules do not reference UTM adapter"

if command -v nix >/dev/null 2>&1; then
  (cd "$REPO_ROOT" && nix flake check --show-trace)
  pass "local nix flake check"
else
  remote "cd '$REMOTE_DIR' && nix flake check --no-write-lock-file --show-trace" >/dev/null
  pass "remote nix flake check"
fi

log "passed checks: $pass_count"
