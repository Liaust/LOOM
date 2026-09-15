#!/usr/bin/env bash
set -euo pipefail

MAIN_HOST="${LOOM_MAIN_HOST:-loom-main}"
LOOM_BIN="${LOOM_BIN:-loom}"
RUN_ID="v0-7-projects-$(date +%Y%m%d%H%M%S)-$RANDOM"
PROJECT_NAME="Portal Acceptance $RUN_ID"
PROJECT_SLUG="portal-acceptance-$RUN_ID"
PROJECT_ROOT="/home/loomadmin/loom-box/Projects/$PROJECT_SLUG"
REMOTE_TMP="/tmp/loom-v0-7-projects-$RUN_ID"
TMP_DIR="${TMPDIR:-/tmp}/loom-v0-7-projects-$RUN_ID"
REGISTERED="false"

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

loom_cmd() {
  # Intentionally allows LOOM_BIN="go run ./cmd/loom" for testing from a repo checkout.
  # shellcheck disable=SC2086
  $LOOM_BIN "$@"
}

remote() {
  ssh -o BatchMode=yes "$MAIN_HOST" "$@"
}

cleanup() {
  if [[ "$REGISTERED" == "true" ]]; then
    remote "loom project archive '$PROJECT_SLUG' --skip-storage-archive --reason 'v0.7 portal project smoke cleanup' >/dev/null 2>&1 || true"
  fi
  remote "rm -rf '$PROJECT_ROOT' '$REMOTE_TMP'" >/dev/null 2>&1 || true
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

mkdir -p "$TMP_DIR"

log "main host: $MAIN_HOST"
log "project slug: $PROJECT_SLUG"
log "project root: $PROJECT_ROOT"

command -v jq >/dev/null || fail "jq is required for smoke assertions"
remote "command -v loom >/dev/null && systemctl is-active loomd >/dev/null"
remote "mkdir -p '$REMOTE_TMP'"
pass "main SSH and loomd reachable"

loom_cmd --json project scaffold "$PROJECT_NAME" \
  --backend \
  --owner-node main \
  --preset minimal \
  --facets notes,docs,tests \
  --slug "$PROJECT_SLUG" >"$TMP_DIR/scaffold.json"

jq -e \
  --arg slug "$PROJECT_SLUG" \
  --arg root "$PROJECT_ROOT" \
  '.slug == $slug and .owner_node == "main" and .project_root == $root and .preset == "minimal"' \
  "$TMP_DIR/scaffold.json" >/dev/null
pass "local client created main-owned backend scaffold"

remote "
  test -d '$PROJECT_ROOT' &&
  test -f '$PROJECT_ROOT/loom.project.yaml' &&
  grep -q 'owner_node: main' '$PROJECT_ROOT/loom.project.yaml' &&
  grep -q 'slug: $PROJECT_SLUG' '$PROJECT_ROOT/loom.project.yaml'
"
pass "main filesystem contains generated project contract"

remote "
  loom project validate '$PROJECT_ROOT' > '$REMOTE_TMP/validate.out' &&
  grep -q 'Project contract: ok' '$REMOTE_TMP/validate.out'
"
pass "main validates generated contract"

remote "
  loom project register '$PROJECT_ROOT' > '$REMOTE_TMP/register.out' &&
  grep -Eq 'Project registration: (created|updated|unchanged)' '$REMOTE_TMP/register.out'
"
REGISTERED="true"
pass "main registers generated project"

remote "
  loom enter --start projects --no-boot-animation --exit-after-render > '$REMOTE_TMP/portal-projects.out' &&
  grep -q 'Project List' '$REMOTE_TMP/portal-projects.out' &&
  grep -q '$PROJECT_NAME' '$REMOTE_TMP/portal-projects.out' &&
  ! grep -q '^Capabilities$' '$REMOTE_TMP/portal-projects.out' &&
  ! grep -q '^Automation$' '$REMOTE_TMP/portal-projects.out' &&
  ! grep -q '^Runtime$' '$REMOTE_TMP/portal-projects.out' &&
  ! grep -q '^Recent Activity$' '$REMOTE_TMP/portal-projects.out'
"
pass "projects portal home renders minimal project list"

remote "
  loom project archive '$PROJECT_SLUG' --skip-storage-archive --reason 'v0.7 portal project smoke cleanup' > '$REMOTE_TMP/archive.out' &&
  grep -Eq 'Project archive: (archived|planned)' '$REMOTE_TMP/archive.out'
"
pass "acceptance project archived after smoke run"

remote "rm -rf '$PROJECT_ROOT'"
REGISTERED="false"
pass "acceptance project folder removed"

log "v0.7 projects surface contextual smoke passed"
