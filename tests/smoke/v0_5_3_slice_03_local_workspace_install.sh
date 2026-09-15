#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
RUN_ID="v0-5-3-slice-03-$(date +%s)-$RANDOM"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-${RUN_ID}.XXXXXX")"
HOME_DIR="$TMP_DIR/home"
RELEASE_ID="release-smoke"
pass_count=0

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

run_loom() {
  if [[ -n "${LOOM_BIN:-}" ]]; then
    "$LOOM_BIN" "$@"
  else
    (cd "$REPO_ROOT" && go run ./cmd/loom "$@")
  fi
}

command -v jq >/dev/null 2>&1 || fail "jq is required"

DRY_JSON="$TMP_DIR/dry-run.json"
APPLY_JSON="$TMP_DIR/apply.json"

run_loom --json setup workspace install \
  --node-key macbook-smoke \
  --display-name "MacBook Smoke Workspace" \
  --main-url http://127.0.0.1:18080 \
  --home-dir "$HOME_DIR" \
  --source-path "$REPO_ROOT" \
  --release-id "$RELEASE_ID" \
  --install-mode user \
  --service-manager none \
  --skip-enroll \
  --dry-run >"$DRY_JSON"

jq -e '.status == "dry_run" and .dry_run == true and .plan.setup.spec.node_kind == "workspace"' "$DRY_JSON" >/dev/null
pass "workspace install dry-run renders local setup plan"

if [[ -e "$HOME_DIR/.local" ]]; then
  fail "dry-run created local install directory"
fi
pass "workspace install dry-run does not mutate temp home"

run_loom --json setup workspace install \
  --node-key macbook-smoke \
  --display-name "MacBook Smoke Workspace" \
  --main-url http://127.0.0.1:18080 \
  --home-dir "$HOME_DIR" \
  --source-path "$REPO_ROOT" \
  --release-id "$RELEASE_ID" \
  --install-mode user \
  --service-manager none \
  --skip-enroll \
  --yes >"$APPLY_JSON"

jq -e '.status == "applied" and .setup.manifest.node_kind == "workspace" and .setup.manifest.enrollment.status == "skipped"' "$APPLY_JSON" >/dev/null
pass "workspace install apply writes skipped-enrollment workspace manifest"

test -x "$HOME_DIR/.local/share/loom/releases/$RELEASE_ID/bin/loom"
test -x "$HOME_DIR/.local/share/loom/releases/$RELEASE_ID/bin/loom-node-agent"
test -L "$HOME_DIR/.local/share/loom/current"
test -L "$HOME_DIR/.local/bin/loom"
test -L "$HOME_DIR/.local/bin/loom-node-agent"
pass "workspace install stages release binaries and symlinks"

test -f "$HOME_DIR/.config/loom/install.yaml"
test -f "$HOME_DIR/.config/loom-node-agent/config.json"
test -f "$HOME_DIR/.local/state/loom-node-agent/state.json"
test -d "$HOME_DIR/loom-box/Dropzone"
test -d "$HOME_DIR/loom-box/.loom/storage/dropzone"
pass "workspace install writes setup state and LOOM Box"

if grep -R -E 'node_enroll_|node_cred_' "$TMP_DIR" >/dev/null; then
  fail "workspace install output or files leaked generated enrollment token patterns"
fi
pass "workspace install smoke output has no generated enrollment token patterns"

printf 'v0.5.3 slice 03 local workspace install smoke passed (%d checks).\n' "$pass_count"
