#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
SMOKE_SLUG="v0-2-slice-03-$RUN_ID"
SMOKE_NAME="v0.2 Slice 03 Smoke $RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/v0_2_slice_03_$RUN_ID.md"
pass_count=0
MOVED_BLOB_PATH=""
BACKUP_BLOB_PATH=""

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

restore_moved_blob() {
  if [[ -n "$MOVED_BLOB_PATH" && -n "$BACKUP_BLOB_PATH" ]]; then
    remote "if test -e '$BACKUP_BLOB_PATH'; then sudo -n -u loom mv '$BACKUP_BLOB_PATH' '$MOVED_BLOB_PATH'; fi" >/dev/null 2>&1 || true
  fi
}
trap restore_moved_blob EXIT

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

json_value() {
  local command="$1"
  local filter="$2"

  remote "$command | jq -r '$filter'"
}

log "target: $HOST"
log "run: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports Slice 03 maintenance migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 18'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_03_workers_list > /tmp/loom-v0-2-slice-03-workers.json"
check_json "worker list includes backup and object-store integrity workers" \
  'cat /tmp/loom-v0-2-slice-03-workers.json' \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.main_backup" and .worker_kind == "main_backup")] | length) == 1
   and ([.data[] | select(.worker_key == "main.object_store_integrity_sample" and .worker_kind == "object_store_integrity")] | length) == 1'

remote "loom worker inspect main.main_backup --json --correlation-id corr_smoke_v0_2_slice_03_backup_inspect > /tmp/loom-v0-2-slice-03-backup-inspect.json"
check_json "main backup worker is inspectable" \
  'cat /tmp/loom-v0-2-slice-03-backup-inspect.json' \
  '.ok == true and .data.instance.worker_key == "main.main_backup" and .data.instance.enabled == true'

remote "loom worker inspect main.object_store_integrity_sample --json --correlation-id corr_smoke_v0_2_slice_03_object_store_inspect > /tmp/loom-v0-2-slice-03-object-store-inspect.json"
check_json "object-store integrity worker is inspectable" \
  'cat /tmp/loom-v0-2-slice-03-object-store-inspect.json' \
  '.ok == true and .data.instance.worker_key == "main.object_store_integrity_sample" and .data.instance.enabled == true'

remote "loom maintenance backup run --once --json --correlation-id corr_smoke_v0_2_slice_03_backup_run > /tmp/loom-v0-2-slice-03-backup-run.json"
check_json "main backup worker run succeeded" \
  'cat /tmp/loom-v0-2-slice-03-backup-run.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.schema_version == "main_backup.result.v0.2"
   and .data.run.result_summary_json.status == "succeeded"
   and .data.run.result_summary_json.artifact_count >= 5
   and .data.health.health_status == "healthy"'
backup_run_id="$(json_value 'cat /tmp/loom-v0-2-slice-03-backup-run.json' '.data.run.worker_run_id')"
backup_operation_id="$(json_value 'cat /tmp/loom-v0-2-slice-03-backup-run.json' '.data.run.result_summary_json.backup_operation_id')"
backup_dir="$(json_value 'cat /tmp/loom-v0-2-slice-03-backup-run.json' '.data.run.result_summary_json.backup_dir')"
[[ "$backup_run_id" == worker_run_* ]] || fail "expected backup worker run id"
[[ "$backup_operation_id" == maintenance_operation_* ]] || fail "expected backup operation id"
[[ -n "$backup_dir" && "$backup_dir" != "null" ]] || fail "expected backup directory"
pass "main backup returned worker run, operation id, and directory"

check_remote "backup directory exists" "sudo -n -u loom test -d '$backup_dir'"
check_remote "backup manifest exists" "sudo -n -u loom test -f '$backup_dir/manifest.json'"
check_remote "backup health report exists" "sudo -n -u loom test -f '$backup_dir/health.json'"
check_remote "backup PostgreSQL dump exists and is non-empty" "sudo -n -u loom test -s '$backup_dir/loom_main.dump'"
check_remote "backup object-store snapshot exists" "sudo -n -u loom test -d '$backup_dir/object-store'"
check_remote "backup private-backups snapshot exists" "sudo -n -u loom test -d '$backup_dir/private-backups'"
check_remote "backup manifest parses" "sudo -n -u loom jq -e '.schema == \"loom.backup.manifest.v0.2\" and .backup_operation_id == \"$backup_operation_id\"' '$backup_dir/manifest.json'"

remote "loom maintenance backup verify '$backup_operation_id' --json --correlation-id corr_smoke_v0_2_slice_03_backup_verify > /tmp/loom-v0-2-slice-03-backup-verify.json"
check_json "backup verification succeeds" \
  'cat /tmp/loom-v0-2-slice-03-backup-verify.json' \
  '.ok == true and .data.status == "succeeded" and .data.backup_operation_id == "'"$backup_operation_id"'"'

remote "loom maintenance backup status --json --correlation-id corr_smoke_v0_2_slice_03_backup_status > /tmp/loom-v0-2-slice-03-backup-status.json"
check_json "backup status reports latest successful backup" \
  'cat /tmp/loom-v0-2-slice-03-backup-status.json' \
  '.ok == true and .data.latest_successful.operation.maintenance_operation_id == "'"$backup_operation_id"'"'

remote "loom maintenance backup list --json --correlation-id corr_smoke_v0_2_slice_03_backup_list > /tmp/loom-v0-2-slice-03-backup-list.json"
check_json "backup list includes created backup operation" \
  'cat /tmp/loom-v0-2-slice-03-backup-list.json' \
  '.ok == true and (.data | type) == "array" and ([.data[] | select(.operation.maintenance_operation_id == "'"$backup_operation_id"'")] | length) == 1'

remote "loom maintenance object-store scan --sample --json --correlation-id corr_smoke_v0_2_slice_03_object_store_scan > /tmp/loom-v0-2-slice-03-object-store-scan.json"
object_store_run_id="$(json_value 'cat /tmp/loom-v0-2-slice-03-object-store-scan.json' '.data.run.worker_run_id')"
[[ "$object_store_run_id" == worker_run_* ]] || fail "expected object-store scan worker run id"
pass "object-store scan returned a worker run id"

check_json "object-store sample scan completes" \
  'cat /tmp/loom-v0-2-slice-03-object-store-scan.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.schema_version == "object_store_integrity.result.v0.2"
   and (.data.run.result_summary_json.checked | type) == "number"
   and .data.health.health_status == "healthy"'

remote "loom maintenance object-store status --json --correlation-id corr_smoke_v0_2_slice_03_object_store_status > /tmp/loom-v0-2-slice-03-object-store-status.json"
check_json "object-store status reports blob counts and latest scan" \
  'cat /tmp/loom-v0-2-slice-03-object-store-status.json' \
  '.ok == true
   and (.data.blob_counts.total | type) == "number"
   and .data.latest_run.worker_run_id == "'"$object_store_run_id"'"'

check_remote "write smoke ingest file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# v0.2 Slice 03 Smoke\n\nObject-store integrity target.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -n -u loom test -r '$INGEST_PATH'"

check_json "create smoke project for targeted object-store scan" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2 Slice 03 backup and integrity smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_slice_03_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name v0-2-slice-03-integrity.md --json --correlation-id corr_smoke_v0_2_slice_03_object_ingest > /tmp/loom-v0-2-slice-03-object-ingest.json"
object_id="$(json_value 'cat /tmp/loom-v0-2-slice-03-object-ingest.json' '.data.object.object.object_id')"
blob_id="$(json_value 'cat /tmp/loom-v0-2-slice-03-object-ingest.json' '.data.object.blob.blob_id')"
blob_path="$(json_value 'cat /tmp/loom-v0-2-slice-03-object-ingest.json' '.data.object.blob.storage_path')"
[[ "$object_id" == object_* ]] || fail "expected object id"
[[ "$blob_id" == blob_* ]] || fail "expected blob id"
[[ -n "$blob_path" && "$blob_path" != "null" ]] || fail "expected blob storage path"
pass "created managed object-store blob for targeted scan"

remote "loom maintenance object-store scan --blob '$blob_id' --json --correlation-id corr_smoke_v0_2_slice_03_object_store_target_initial > /tmp/loom-v0-2-slice-03-object-store-target-initial.json"
check_json "targeted object-store scan verifies managed blob" \
  'cat /tmp/loom-v0-2-slice-03-object-store-target-initial.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.target_blob_ref == "'"$blob_id"'"
   and .data.run.result_summary_json.verified == 1'

MOVED_BLOB_PATH="$blob_path"
BACKUP_BLOB_PATH="$blob_path.smoke-$RUN_ID"
remote "sudo -n -u loom mv '$MOVED_BLOB_PATH' '$BACKUP_BLOB_PATH'"
pass "temporarily moved managed blob file for missing-blob simulation"

remote "loom maintenance object-store scan --blob '$blob_id' --json --correlation-id corr_smoke_v0_2_slice_03_object_store_target_missing > /tmp/loom-v0-2-slice-03-object-store-target-missing.json"
check_json "targeted scan detects missing managed blob" \
  'cat /tmp/loom-v0-2-slice-03-object-store-target-missing.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.missing == 1
   and .data.run.result_summary_json.status == "critical"'

remote "loom maintenance findings list --worker object_store_integrity --status open --severity critical --json --correlation-id corr_smoke_v0_2_slice_03_findings_missing > /tmp/loom-v0-2-slice-03-findings-missing.json"
check_json "missing blob creates critical maintenance finding" \
  'cat /tmp/loom-v0-2-slice-03-findings-missing.json' \
  '.ok == true
   and ([.data[] | select(.finding_kind == "object_store_blob_missing" and .subject_id == "'"$blob_id"'")] | length) == 1'

remote "sudo -n -u loom mv '$BACKUP_BLOB_PATH' '$MOVED_BLOB_PATH'"
BACKUP_BLOB_PATH=""
pass "restored managed blob file after missing-blob simulation"

remote "loom maintenance object-store scan --blob '$blob_id' --json --correlation-id corr_smoke_v0_2_slice_03_object_store_target_restored > /tmp/loom-v0-2-slice-03-object-store-target-restored.json"
check_json "targeted scan verifies restored blob and resolves finding" \
  'cat /tmp/loom-v0-2-slice-03-object-store-target-restored.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.verified == 1
   and .data.run.result_summary_json.findings_resolved >= 1'

check_json "restored object blob status is verified" \
  "loom object inspect '$object_id' --json --correlation-id corr_smoke_v0_2_slice_03_object_inspect_restored" \
  ".ok == true and .data.blob.blob_id == \"$blob_id\" and .data.blob.status == \"verified\""

log "completed $pass_count checks"
