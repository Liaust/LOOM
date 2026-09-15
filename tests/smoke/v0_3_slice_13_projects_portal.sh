#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-13-$(date +%s)-$RANDOM"
SLUG="smoke-portal-$RUN_ID"
SAFE_SLUG="$(printf '%s' "$SLUG" | sed -E 's/[^[:alnum:]]+/_/g; s/^_//; s/_$//')"
CAPABILITY="main@$SLUG.hello_world"
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

check_json() {
  local label="$1"
  local command="$2"
  local filter="$3"

  remote "$command | jq -e '$filter' >/dev/null"
  pass "$label"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"
log "project slug: $SLUG"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project portal era migrations" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 33'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-13.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
log "project root: $PROJECT_ROOT"
log "capability: $CAPABILITY"

check_remote "scaffold portal project" "
  loom project scaffold 'Smoke Portal $RUN_ID' \
    --owner-node main \
    --preset automation \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "enable script exposure for project-owned capability" "
  perl -0pi -e 's/enabled: false/enabled: true/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/default_mode: wait_until_started/default_mode: wait_for_completion/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/wait_timeout_seconds: 10/wait_timeout_seconds: 30/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
"

check_remote "validate and register portal project" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out'
  grep -Eq 'Project registration: (created|updated|unchanged)' '$TMP_DIR/register.out'
"

check_remote "activate scripts facet for portal project" "
  loom project activate '$SLUG' --facet scripts > '$TMP_DIR/activate.out'
  grep -q 'Facet: scripts activated' '$TMP_DIR/activate.out'
  grep -q '$CAPABILITY' '$TMP_DIR/activate.out'
"

check_remote "projects portal renders live project data" "
  loom enter --start projects --no-boot-animation --exit-after-render > '$TMP_DIR/portal-projects.out'
  grep -q 'Projects' '$TMP_DIR/portal-projects.out'
  grep -q '$SLUG' '$TMP_DIR/portal-projects.out'
  grep -q 'Facets' '$TMP_DIR/portal-projects.out'
  grep -q 'Capabilities' '$TMP_DIR/portal-projects.out'
  grep -q '$CAPABILITY' '$TMP_DIR/portal-projects.out'
  grep -q 'Data Policy' '$TMP_DIR/portal-projects.out'
  grep -q 'Runtime' '$TMP_DIR/portal-projects.out'
"

check_remote "projects scoped search finds project" "
  loom enter --search '#projects $SLUG' --no-boot-animation --exit-after-render > '$TMP_DIR/portal-search-project.out'
  grep -q '$SLUG' '$TMP_DIR/portal-search-project.out'
  grep -q 'project' '$TMP_DIR/portal-search-project.out'
"

check_remote "projects scoped search finds project capability URL" "
  loom enter --search '#projects $CAPABILITY' --no-boot-animation --exit-after-render > '$TMP_DIR/portal-search-capability.out'
  grep -q 'main@$SLUG' '$TMP_DIR/portal-search-capability.out'
  grep -q 'Hello World' '$TMP_DIR/portal-search-capability.out'
"

check_remote "project registration plan action previews from portal" "
  loom enter \
    --start projects \
    --preview-action 'project.${SAFE_SLUG}.registration_plan' \
    --no-boot-animation \
    --exit-after-render > '$TMP_DIR/portal-plan-preview.out'
  grep -q 'View Registration Plan' '$TMP_DIR/portal-plan-preview.out'
  grep -q 'loom project status $SLUG' '$TMP_DIR/portal-plan-preview.out'
"

check_remote "project watch plan action runs from portal" "
  loom enter \
    --start projects \
    --run-action 'project.${SAFE_SLUG}.watch_plan' \
    --no-boot-animation \
    --exit-after-render > '$TMP_DIR/portal-watch-run.out'
  grep -q 'Project watch plan loaded' '$TMP_DIR/portal-watch-run.out'
"

check_remote "project activation action runs with confirmation from portal" "
  loom enter \
    --start projects \
    --run-action 'project.${SAFE_SLUG}.activate.scripts' \
    --confirm \
    --no-boot-animation \
    --exit-after-render > '$TMP_DIR/portal-activate-run.out'
  grep -q 'Project activation completed' '$TMP_DIR/portal-activate-run.out'
"

check_json "project status still records active script exposure" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   ([.data.script_exposures[] | select(.script_key == \"hello_world\" and .activation_status == \"active\" and .capability_address == \"$CAPABILITY\")] | length) == 1"

pass "v0.3 Slice 13 projects portal smoke completed with $pass_count checks"
