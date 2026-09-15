#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'v0.5.3 slice 04 workspace update smoke: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

loom_cli() {
  if [[ -n "${LOOM_BIN:-}" ]]; then
    "$LOOM_BIN" "$@"
  else
    (cd "$REPO_ROOT" && go run ./cmd/loom "$@")
  fi
}

write_release() {
  local dir="$1"
  local id="$2"
  mkdir -p "$dir/bin"
  printf '#!/bin/sh\n' >"$dir/bin/loom"
  printf '#!/bin/sh\n' >"$dir/bin/loom-node-agent"
  chmod 0755 "$dir/bin/loom" "$dir/bin/loom-node-agent"
  cat >"$dir/loom-release.yaml" <<EOF
schema_version: loom.release.v0.5.1
release_id: $id
version: 0.5.3-smoke
commit: $id-commit
source_path: $dir
metadata:
  smoke: v0.5.3-slice-04
EOF
}

assert_link() {
  local link="$1"
  local want="$2"
  local got
  got="$(readlink "$link")"
  if [[ "$got" != /* ]]; then
    got="$(dirname "$link")/$got"
  fi
  got="$(cd "$(dirname "$got")" && pwd -P)/$(basename "$got")"
  want="$(cd "$(dirname "$want")" && pwd -P)/$(basename "$want")"
  [[ "$got" == "$want" ]] || fail "symlink $link points at $got, want $want"
}

require_command jq

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-5-3-workspace-update.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

HOME_DIR="$TMP_DIR/home"
ACTIVE="$TMP_DIR/releases/active"
TARGET="$TMP_DIR/releases/target"
CURRENT="$HOME_DIR/.local/share/loom/current"
STATE="$HOME_DIR/.local/state/loom/update"

write_release "$ACTIVE" "active"
write_release "$TARGET" "target"
mkdir -p "$(dirname "$CURRENT")"
ln -s "$ACTIVE" "$CURRENT"

loom_cli --json update workspace plan \
  --release-path "$TARGET" \
  --home-dir "$HOME_DIR" \
  --service-manager none \
  --skip-service-restart \
  --skip-health-check \
  | jq -e '
      .schema_version == "loom.update.workspace.v0.5.3"
      and .backup.required == false
      and .rollback.class == "service_only_possible"
      and ([.steps[].id] | index("nixos_rebuild") | not)
    ' >/dev/null

loom_cli --json update workspace apply \
  --release-path "$TARGET" \
  --home-dir "$HOME_DIR" \
  --service-manager none \
  --skip-service-restart \
  --skip-health-check \
  --yes \
  | jq -e '
      .status == "succeeded"
      and (.changed[] | select(.step == "switch_release" and .status == "changed"))
      and (.changed[] | select(.step == "workspace_health" and .status == "skipped"))
    ' >/dev/null

assert_link "$CURRENT" "$TARGET"
assert_link "$HOME_DIR/.local/bin/loom" "$CURRENT/bin/loom"
assert_link "$HOME_DIR/.local/bin/loom-node-agent" "$CURRENT/bin/loom-node-agent"

loom_cli --json update workspace status --home-dir "$HOME_DIR" \
  | jq -e '.active_exists == true and .active.status == "succeeded" and (.history | length) >= 1' >/dev/null

loom_cli --json update workspace rollback \
  --home-dir "$HOME_DIR" \
  --service-manager none \
  --skip-service-restart \
  --skip-health-check \
  --yes \
  | jq -e '
      .status == "rolled_back"
      and (.changed[] | select(.step == "switch_release" and .status == "changed"))
    ' >/dev/null

assert_link "$CURRENT" "$ACTIVE"

loom_cli --json update workspace history --home-dir "$HOME_DIR" \
  | jq -e '(.history | length) >= 2' >/dev/null

printf '[smoke] v0.5.3 slice 04 workspace update smoke passed\n'
