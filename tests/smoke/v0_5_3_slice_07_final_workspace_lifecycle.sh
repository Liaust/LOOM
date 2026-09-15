#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'v0.5.3 slice 07 final workspace lifecycle: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

if [[ "${LOOM_RUN_PRODUCTION_MACBOOK_SMOKE:-}" != "1" ]]; then
  fail "refusing to run production MacBook lifecycle smoke without LOOM_RUN_PRODUCTION_MACBOOK_SMOKE=1"
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOME_DIR="${LOOM_WORKSPACE_HOME:-$HOME}"
NODE_KEY="${LOOM_WORKSPACE_NODE_KEY:-macbook}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
MANIFEST_PATH="${LOOM_INSTALL_MANIFEST:-$HOME_DIR/.config/loom/install.yaml}"
UPDATE_STATE="${LOOM_WORKSPACE_UPDATE_STATE:-$HOME_DIR/.local/state/loom/update}"
CURRENT_PATH="${LOOM_WORKSPACE_CURRENT:-$HOME_DIR/.local/share/loom/current}"
LOOM_BIN="${LOOM_BIN:-$HOME_DIR/.local/bin/loom}"

require_command jq

"$SCRIPT_DIR/v0_5_3_slice_05_macbook_acceptance.sh"
LOOM_RUN_PRODUCTION_MACBOOK_SMOKE=1 "$SCRIPT_DIR/v0_5_3_slice_06_workspace_usefulness.sh"

"$LOOM_BIN" --json setup uninstall plan \
  --manifest "$MANIFEST_PATH" \
  --mode disable-only \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  | jq -e '
      .schema_version == "loom.setup.uninstall.v0.5.2"
      and .mode == "disable-only"
      and ([.services[]? | select(.manager == "launchd" and .action == "stop_disable")] | length) >= 1
      and all(.paths[]?; .action == "preserve")
    ' >/dev/null
pass "disable-only uninstall plan is read-only and preserves local data"

"$LOOM_BIN" --json setup uninstall apply \
  --manifest "$MANIFEST_PATH" \
  --mode disable-only \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  --dry-run \
  --yes \
  | jq -e '
      .dry_run == true
      and .mode == "disable-only"
      and .refused == false
      and ((.blocked // []) | length == 0)
      and ([.changed[]? | select(.category == "service" and .status == "would_change")] | length) >= 1
    ' >/dev/null
pass "disable-only uninstall dry-run would unload LaunchAgent without mutating"

"$LOOM_BIN" --json setup uninstall plan \
  --manifest "$MANIFEST_PATH" \
  --mode preserve-data \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  --skip-main-revoke \
  | jq -e '
      .mode == "preserve-data"
      and ([.paths[]? | select(.id == "box" and .action == "preserve")] | length) == 1
      and ([.paths[]? | select(.id == "launch_agent_plist" and .action == "remove")] | length) == 1
      and ([.paths[]? | select(.id == "node_agent_config" and .action == "remove")] | length) == 1
    ' >/dev/null
pass "preserve-data uninstall plan removes generated integration and keeps LOOM Box"

"$LOOM_BIN" --json setup uninstall plan \
  --manifest "$MANIFEST_PATH" \
  --mode purge \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  | jq -e '
      .mode == "purge"
      and ([.guardrails[]? | select(.id == "confirm_node" and .required == true and .satisfied == false)] | length) == 1
    ' >/dev/null
pass "purge plan requires typed node confirmation"

"$LOOM_BIN" --json setup uninstall plan \
  --manifest "$MANIFEST_PATH" \
  --mode purge \
  --confirm-node "$NODE_KEY" \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  | jq -e '
      .mode == "purge"
      and ([.guardrails[]? | select(.id == "confirm_node" and .satisfied == true)] | length) == 1
      and ([.paths[]? | select(.id == "box" and .action == "preserve")] | length) == 1
    ' >/dev/null
pass "purge plan keeps LOOM Box unless --remove-box is explicit"

"$LOOM_BIN" --json setup repair \
  --manifest "$MANIFEST_PATH" \
  --home-dir "$HOME_DIR" \
  --install-mode service \
  --service-manager launchd \
  --dry-run \
  --fix all-safe \
  | jq -e '
      .dry_run == true
      and .refused == false
      and (((.doctor.findings // []) | map(select(.severity == "blocking" or .severity == "error")) | length) == 0)
    ' >/dev/null
pass "setup repair dry-run has no blocking/error findings"

"$LOOM_BIN" --json update workspace status --state-dir "$UPDATE_STATE" \
  | jq -e '.state_dir == "'"$UPDATE_STATE"'"' >/dev/null
pass "workspace update status is readable"

if "$LOOM_BIN" --json update workspace status --state-dir "$UPDATE_STATE" | jq -e '.active != null' >/dev/null; then
  "$LOOM_BIN" --json update workspace rollback \
    --state-dir "$UPDATE_STATE" \
    --home-dir "$HOME_DIR" \
    --active-path "$CURRENT_PATH" \
    --to "$CURRENT_PATH" \
    --skip-service-restart \
    --skip-health-check \
    --dry-run \
    --yes \
    | jq -e '.status == "dry_run" and .refused == false' >/dev/null
  pass "workspace rollback dry-run is available"
else
  pass "workspace rollback dry-run skipped because there is no active workspace update manifest"
fi

printf 'v0.5.3 slice 07 final workspace lifecycle passed.\n'
