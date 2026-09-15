#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOOM_CMD="${LOOM_CMD:-go run ./cmd/loom}"
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

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
  fi
}

loom() {
  (cd "$ROOT" && $LOOM_CMD "$@")
}

require_command jq

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

VALID="$TMP_DIR/gmail-automation"
mkdir -p "$VALID/scripts" "$VALID/direct_events" "$VALID/notes"
cat >"$VALID/loom.project.yaml" <<'YAML'
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  notes: true
  scripts: true
  direct_events: true
YAML

log "validating valid project contract"
loom project validate "$VALID" >"$TMP_DIR/valid.out"
grep -q "Project contract: ok" "$TMP_DIR/valid.out"
grep -q "Provider: macbook@gmail-automation" "$TMP_DIR/valid.out"
pass "human validate succeeds"

loom --json project validate "$VALID" \
  | jq -e '.ok == true and .project.slug == "gmail-automation" and .derived_providers[0].compact_address == "macbook@gmail-automation"' >/dev/null
pass "json validate succeeds"

loom project plan "$VALID" >"$TMP_DIR/plan.out"
grep -q "would_create_or_update_project" "$TMP_DIR/plan.out"
grep -q "would_scan_facet" "$TMP_DIR/plan.out"
pass "human plan succeeds"

loom --json project plan "$VALID" \
  | jq -e '.registerable == true and (.actions | length) > 0 and .project.owner_node == "macbook"' >/dev/null
pass "json plan succeeds"

INVALID="$TMP_DIR/invalid"
mkdir -p "$INVALID"
cat >"$INVALID/loom.project.yaml" <<'YAML'
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: Gmail Automation
  name: Gmail Automation
  owner_node: macbook
YAML

set +e
loom project validate "$INVALID" >"$TMP_DIR/invalid.out" 2>"$TMP_DIR/invalid.err"
status=$?
set -e
test "$status" -ne 0
grep -q "Project contract: failed" "$TMP_DIR/invalid.out"
grep -q "project.slug_invalid" "$TMP_DIR/invalid.out"
pass "invalid contract fails"

WARNING="$TMP_DIR/warning"
mkdir -p "$WARNING"
cat >"$WARNING/loom.project.yaml" <<'YAML'
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  workflows: true
YAML

loom project validate "$WARNING" >"$TMP_DIR/warning.out"
grep -q "workflow.placeholder" "$TMP_DIR/warning.out"
pass "warning-only contract succeeds by default"

set +e
loom project validate "$WARNING" --strict >"$TMP_DIR/warning-strict.out" 2>"$TMP_DIR/warning-strict.err"
status=$?
set -e
test "$status" -ne 0
grep -q "workflow.placeholder" "$TMP_DIR/warning-strict.out"
pass "strict mode fails warnings"

log "completed $pass_count checks"
