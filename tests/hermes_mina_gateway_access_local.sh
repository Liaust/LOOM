#!/usr/bin/env bash
# Disposable namespace proof only; never invoke against Main or canonical data.
set -euo pipefail
if [[ "${1-}" == --worker ]]; then
  root="$2"; lane="$3"
  export HOME="$root/workspace"
  [[ "$(id -u)" == 65534 ]]
  [[ "$(stat -c %i "$root/box/project/source.txt")" == "$(cat "$root/inode")" ]]
  printf '%s\n' "$lane" >> "$root/box/project/source.txt"
  mkdir -p "$root/box/project/node_modules/fixture"
  printf '%s\n' dependency > "$root/box/project/node_modules/fixture/index.js"
  printf '%s\n' "$lane" > "$root/workspace/own.txt"
  git -C "$root/box/project" status --porcelain >/dev/null
  git -C "$root/box/review" status --porcelain >/dev/null
  if [[ "$lane" == gateway ]]; then
    for path in "$root/protected/recovery" "$root/protected/keys" "$root/protected/database" "$root/workspace/skills/installed"; do
      if (printf '%s\n' forbidden > "$path/write") 2>/dev/null; then
        echo "sandbox allowed unrelated write" >&2; exit 1
      fi
    done
  fi
  exit 0
fi
[[ "$(uname -s)" == Linux && "$(id -u)" == 0 ]] || { echo 'Linux root/systemd fixture required' >&2; exit 1; }
[[ "$(hostname)" != loom-main && ! -e /var/lib/loom && ! -e /srv/loom ]] || { echo 'Refusing canonical LOOM host' >&2; exit 1; }
[[ "${LOOM_DISPOSABLE_NAMESPACE_TEST-}" == yes ]] || { echo 'Explicit disposable namespace test required' >&2; exit 1; }
for tool in systemd-run runuser git stat; do command -v "$tool" >/dev/null; done
root="$(mktemp -d /run/loom-mina-gateway-fixture-XXXXXX)"
identity="$(stat -c '%d:%i' "$root")"
unit="$(basename "$root")"
cleanup() {
  systemctl stop "$unit.service" "$unit-missing.service" >/dev/null 2>&1 || true
  systemctl reset-failed "$unit.service" "$unit-missing.service" >/dev/null 2>&1 || true
  [[ "$(stat -c '%d:%i' "$root")" == "$identity" && ! -L "$root" ]]
  rm -rf -- "$root"
  [[ ! -e "$root" ]]
}
trap cleanup EXIT
chmod 0755 "$root"
install -m 0755 "$0" "$root/probe.sh"
mkdir -p "$root/box/project" "$root/workspace/skills/installed" "$root/protected/"{keys,database,recovery}
chmod -R a+rwX "$root/box" "$root/workspace" "$root/protected"
chown -R 65534:65534 "$root/box" "$root/workspace"
runuser -u nobody -- git -C "$root/box/project" init -q
runuser -u nobody -- touch "$root/box/project/source.txt"
runuser -u nobody -- git -C "$root/box/project" add source.txt
runuser -u nobody -- git -C "$root/box/project" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
runuser -u nobody -- git -C "$root/box/project" worktree add -qb review "$root/box/review"
stat -c %i "$root/box/project/source.txt" > "$root/inode"
runuser -u nobody -- bash "$root/probe.sh" --worker "$root" tui
scope=(-p User=65534 -p Group=65534 -p ProtectSystem=strict -p ProtectHome=true
       -p NoNewPrivileges=true -p PrivateNetwork=true -p PrivateTmp=true
       -p "ReadWritePaths=$root/box $root/workspace"
       -p "ReadOnlyPaths=$root/workspace/skills/installed"
       -p "RequiresMountsFor=$root/box $root/workspace"
       -p "AssertPathIsDirectory=$root/box")
systemd-run --quiet --wait --collect --unit="$unit" "${scope[@]}" bash "$root/probe.sh" --worker "$root" gateway
[[ "$(cat "$root/box/project/source.txt")" == $'tui\ngateway' ]]
[[ "$(cat "$root/workspace/own.txt")" == gateway ]]
if systemd-run --quiet --wait --collect --unit="$unit-missing" "${scope[@]}" \
    -p "AssertPathIsDirectory=$root/absent-volume/box" \
    -p "ReadWritePaths=$root/absent-volume/box" \
    bash "$root/probe.sh" --worker "$root" forbidden; then
  echo 'Missing mounted Box was admitted' >&2; exit 1
fi
[[ ! -e "$root/absent-volume" && "$(cat "$root/workspace/own.txt")" == gateway ]]
echo 'PASS gateway namespace: same inode, edit/dependency/Git worktree, protected sentinels, missing Box refusal'
