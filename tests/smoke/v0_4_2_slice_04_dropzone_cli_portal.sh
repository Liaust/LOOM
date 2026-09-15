#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
RUN_ID="v0-4-2-slice-04-$(date +%s)-$RANDOM"
REMOTE_TMP="/tmp/loom-dropzone-slice-04-${RUN_ID}"
REMOTE_BOX="${REMOTE_TMP}/LOOM Box"
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

main_ssh() {
  ssh -o BatchMode=yes "$MAIN_HOST" "$@"
}

loom_main() {
  main_ssh "sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env LOOM_BOX_PATH='$REMOTE_BOX' LOOM_BOX_PROFILE=workspace loom $*"
}

check_json_value() {
  local label="$1"
  local json="$2"
  local filter="$3"

  if ! printf '%s' "$json" | jq -e "$filter" >/dev/null; then
    printf '%s\n' "$json" >&2
    fail "$label"
  fi
  pass "$label"
}

require_command jq
require_command ssh

log "main: $MAIN_HOST"
log "remote Box: $REMOTE_BOX"

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'
pass "main health ok"

main_ssh "sudo rm -rf '$REMOTE_TMP' && sudo -u loom mkdir -p '$REMOTE_TMP'"
cleanup() {
  main_ssh "sudo rm -rf '$REMOTE_TMP'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

BOX_INIT_JSON="$(loom_main "--json box init --path '$REMOTE_BOX' --profile workspace")"
check_json_value "workspace-profile Box initialized on main for CLI/Portal smoke" "$BOX_INIT_JSON" \
  '.status_after.state == "ok" and .status_after.dropzone_transfers.runtime_state == "active"'
BOX_ID="$(printf '%s' "$BOX_INIT_JSON" | jq -r '.status_after.contract.box_id')"
[[ "$BOX_ID" == box_* ]] || fail "expected Box id"

main_ssh "sudo -u loom bash -s" <<REMOTE
set -euo pipefail
REMOTE_BOX='$REMOTE_BOX'
BOX_ID='$BOX_ID'
FAILED_TIME='2026-01-01T00:00:00Z'
ACCEPTED_TIME='2026-01-02T00:00:00Z'
OLD_ACCEPTED_TIME="\$(date -u -d '8 days ago' +%Y-%m-%dT%H:%M:%SZ)"
mkdir -p "\$REMOTE_BOX/Dropzone" "\$REMOTE_BOX/.loom/state/dropzone/transfers"
printf 'failed payload for slice 04\n' > "\$REMOTE_BOX/Dropzone/failed.bin"
printf 'accepted payload for slice 04\n' > "\$REMOTE_BOX/Dropzone/accepted.bin"
touch -d "\$FAILED_TIME" "\$REMOTE_BOX/Dropzone/failed.bin"
touch -d "\$ACCEPTED_TIME" "\$REMOTE_BOX/Dropzone/accepted.bin"
FAILED_SIZE="\$(stat -c %s "\$REMOTE_BOX/Dropzone/failed.bin")"
ACCEPTED_SIZE="\$(stat -c %s "\$REMOTE_BOX/Dropzone/accepted.bin")"
jq -n \
  --arg schema 'loom.dropzone.transfer.v0.4.2' \
  --arg transfer_id 'drop_failed' \
  --arg source_node_key 'slice04-node' \
  --arg source_box_id "\$BOX_ID" \
  --arg root "\$REMOTE_BOX" \
  --arg local_path "\$REMOTE_BOX/Dropzone/failed.bin" \
  --arg rel 'failed.bin' \
  --arg name 'failed.bin' \
  --arg mod_time "\$FAILED_TIME" \
  --arg now "\$FAILED_TIME" \
  --argjson size "\$FAILED_SIZE" \
  '{
    schema_version: \$schema,
    transfer_id: \$transfer_id,
    source_node_key: \$source_node_key,
    source_box_id: \$source_box_id,
    source_box_root_path: \$root,
    local_absolute_path: \$local_path,
    relative_dropzone_path: \$rel,
    file_name: \$name,
    file_kind: "file",
    file_size_bytes: \$size,
    mod_time: \$mod_time,
    checksum_algorithm: "sha256",
    chunk_size_bytes: 7,
    total_chunks: 4,
    uploaded_bytes: 0,
    status: "failed",
    failure_code: "dropzone.upload_failed",
    failure_message: "controlled smoke failure",
    retry_count: 1,
    discovered_at: \$now,
    first_seen_at: \$now,
    last_observed_at: \$now,
    updated_at: \$now,
    safe_to_delete: false
  }' > "\$REMOTE_BOX/.loom/state/dropzone/transfers/drop_failed.json"
jq -n \
  --arg schema 'loom.dropzone.transfer.v0.4.2' \
  --arg transfer_id 'drop_accepted' \
  --arg source_node_key 'slice04-node' \
  --arg source_box_id "\$BOX_ID" \
  --arg root "\$REMOTE_BOX" \
  --arg local_path "\$REMOTE_BOX/Dropzone/accepted.bin" \
  --arg rel 'accepted.bin' \
  --arg name 'accepted.bin' \
  --arg mod_time "\$ACCEPTED_TIME" \
  --arg accepted_at "\$OLD_ACCEPTED_TIME" \
  --arg remote_path '/var/lib/loom/LOOM Box/.loom/storage/dropzone/slice04/drop_accepted/accepted.bin' \
  --argjson size "\$ACCEPTED_SIZE" \
  '{
    schema_version: \$schema,
    transfer_id: \$transfer_id,
    source_node_key: \$source_node_key,
    source_box_id: \$source_box_id,
    source_box_root_path: \$root,
    local_absolute_path: \$local_path,
    relative_dropzone_path: \$rel,
    file_name: \$name,
    file_kind: "file",
    file_size_bytes: \$size,
    mod_time: \$mod_time,
    checksum_algorithm: "sha256",
    checksum_value: "smoke",
    chunk_size_bytes: 7,
    total_chunks: 4,
    uploaded_bytes: \$size,
    uploaded_chunks: [0,1,2,3],
    status: "accepted",
    retry_count: 0,
    discovered_at: \$mod_time,
    first_seen_at: \$mod_time,
    last_observed_at: \$mod_time,
    updated_at: \$accepted_at,
    accepted_at: \$accepted_at,
    remote_storage_path: \$remote_path,
    main_custody_id: "drop_custody_slice04",
    safe_to_delete: true
  }' > "\$REMOTE_BOX/.loom/state/dropzone/transfers/drop_accepted.json"
REMOTE
pass "controlled Dropzone transfer records created"

STATUS_JSON="$(loom_main "--json box dropzone status --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone status sees failed and accepted records" "$STATUS_JSON" \
  '.runtime_state == "active" and .counts.failed == 1 and .counts.accepted == 1 and .accepted[0].safe_to_delete == true'

LIST_JSON="$(loom_main "--json box dropzone list --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone list returns stable transfer summaries" "$LIST_JSON" \
  '.transfers | map(.transfer_id) | index("drop_failed") and index("drop_accepted")'

INSPECT_JSON="$(loom_main "--json box dropzone inspect drop_accepted --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone inspect shows custody target" "$INSPECT_JSON" \
  '.record.status == "accepted" and .record.safe_to_delete == true and .record.main_custody_id == "drop_custody_slice04"'

PORTAL_BOX="$(loom_main "enter --start box --exit-after-render --no-boot-animation")"
printf '%s' "$PORTAL_BOX" | grep -q 'Dropzone Transfers' || fail "Portal Box render missing Dropzone Transfers"
printf '%s' "$PORTAL_BOX" | grep -q 'Needs attention' || fail "Portal Box render missing failed Dropzone group"
printf '%s' "$PORTAL_BOX" | grep -q 'Accepted - safe to delete locally' || fail "Portal Box render missing accepted safe-to-delete group"
pass "Portal Box render shows Dropzone state groups"

PORTAL_SEARCH="$(loom_main "enter --start box --exit-after-render --no-boot-animation --search '#box failed.bin'")"
printf '%s' "$PORTAL_SEARCH" | grep -q 'failed.bin' || fail "Portal #box search missing failed Dropzone transfer"
pass "Portal #box search finds Dropzone transfer records"

RETRY_JSON="$(loom_main "--json box dropzone retry drop_failed --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone retry makes failed transfer runnable" "$RETRY_JSON" \
  '.action == "retry" and .before.status == "failed" and .after.status == "ready" and ((.after.failure_message // "") == "")'

PAUSE_JSON="$(loom_main "--json box dropzone pause drop_failed --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone pause marks transfer paused" "$PAUSE_JSON" \
  '.action == "pause" and .after.status == "paused"'

RESUME_JSON="$(loom_main "--json box dropzone resume drop_failed --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone resume marks transfer runnable" "$RESUME_JSON" \
  '.action == "resume" and .after.status == "ready"'

CLEAN_JSON="$(loom_main "--json box dropzone clean --accepted --older-than 7d --path '$REMOTE_BOX' --profile workspace")"
check_json_value "CLI dropzone clean removes old accepted state only" "$CLEAN_JSON" \
  '.removed_count == 1 and (.safe_local_delete_note | contains("never deletes user files"))'
main_ssh "sudo -u loom test -f '$REMOTE_BOX/Dropzone/accepted.bin'"
pass "clean accepted did not delete local Dropzone file"

log "completed $pass_count checks"
