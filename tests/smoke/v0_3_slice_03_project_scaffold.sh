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

loom() {
  (cd "$ROOT" && $LOOM_CMD "$@")
}

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

check_file() {
  local root="$1"
  local path="$2"
  test -f "$root/$path" || fail "missing expected file: $root/$path"
}

check_executable() {
  local root="$1"
  local path="$2"
  test -x "$root/$path" || fail "missing expected executable: $root/$path"
}

scaffold_and_validate() {
  local preset="$1"
  local slug="smoke-$preset"
  local root="$TMP_DIR/$slug"

  log "scaffold preset: $preset"
  loom project scaffold "Smoke $preset" --owner-node main --preset "$preset" --slug "$slug" --directory "$TMP_DIR" >"$TMP_DIR/$preset.out"
  grep -q "Project scaffold: created" "$TMP_DIR/$preset.out"
  check_file "$root" "loom.project.yaml"
  check_file "$root" "README.md"
  check_file "$root" "AGENTS.md"
  check_file "$root" "tests/validate_project.sh"
  check_executable "$root" "tests/validate_project.sh"

  loom project validate "$root" >"$TMP_DIR/$preset.validate.out"
  grep -q "Project contract: ok" "$TMP_DIR/$preset.validate.out"
  loom project plan "$root" >"$TMP_DIR/$preset.plan.out"
  grep -q "Project plan:" "$TMP_DIR/$preset.plan.out"

  case "$preset" in
    minimal)
      check_file "$root" "notes/loom.notes.yaml"
      ;;
    research)
      check_file "$root" "notes/loom.notes.yaml"
      check_file "$root" "docs/AGENTS.md"
      check_file "$root" "policies/sync.yaml"
      check_file "$root" "policies/backup.yaml"
      check_file "$root" "policies/workers.yaml"
      ;;
    automation)
      check_file "$root" "scripts/hello_world/loom.script.yaml"
      check_file "$root" "scripts/hello_world/loom.exposure.yaml"
      check_executable "$root" "scripts/hello_world/run.sh"
      check_file "$root" "schedules/example_schedule/loom.schedule.yaml"
      check_file "$root" "direct_events/example_event/loom.direct_event.yaml"
      check_file "$root" "policies/credentials.yaml"
      ;;
    connector)
      check_file "$root" "connectors/example_connector/loom.connector.yaml"
      check_file "$root" "connectors/example_connector/scripts/ping/loom.script.yaml"
      check_executable "$root" "connectors/example_connector/scripts/ping/run.sh"
      check_file "$root" "scripts/hello_world/loom.script.yaml"
      ;;
    module)
      check_file "$root" "modules/example_module/module.json"
      check_file "$root" "modules/example_module/loom.module_project.yaml"
      check_file "$root" "connectors/example_connector/loom.connector.yaml"
      check_file "$root" "connectors/example_connector/scripts/ping/loom.script.yaml"
      ;;
  esac
  pass "$preset scaffold validates and plans"
}

for preset in minimal research automation connector module; do
  scaffold_and_validate "$preset"
done

loom --json project scaffold "Explicit Facets" \
  --owner-node main \
  --facets notes,scripts,direct-events \
  --directory "$TMP_DIR" \
  | jq -e '.ok == true and .slug == "explicit-facets" and (.facets == ["notes","scripts","direct_events"])' >/dev/null
pass "explicit facets normalize and override preset"

loom project scaffold "Dry Run Project" --owner-node main --directory "$TMP_DIR" --dry-run >"$TMP_DIR/dry-run.out"
grep -q "Project scaffold: planned" "$TMP_DIR/dry-run.out"
test ! -e "$TMP_DIR/dry-run-project" || fail "dry run wrote project root"
pass "dry-run writes nothing"

set +e
loom project scaffold "Smoke minimal" --owner-node main --preset minimal --slug smoke-minimal --directory "$TMP_DIR" >"$TMP_DIR/existing.out" 2>"$TMP_DIR/existing.err"
status=$?
set -e
test "$status" -ne 0
pass "existing destination fails without force"

loom project scaffold "Smoke minimal" --owner-node main --preset minimal --slug smoke-minimal --directory "$TMP_DIR" --force >"$TMP_DIR/force.out"
grep -q "Project scaffold: created" "$TMP_DIR/force.out"
pass "force overwrites generated files"

log "completed $pass_count checks"
