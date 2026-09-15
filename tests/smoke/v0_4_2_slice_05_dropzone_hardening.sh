#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
RUN_ID="v0-4-2-slice-05-$(date +%s)-$RANDOM"
MAIN_BOX="/var/lib/loom/LOOM Box"
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

remote() {
  ssh -o BatchMode=yes "$HOST" "$@"
}

check_remote() {
  local label="$1"
  local command="$2"

  remote "$command" >/dev/null
  pass "$label"
}

require_command ssh
require_command jq

log "target: $HOST"
log "run id: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loomd Unix socket exists" 'test -S /run/loom/loomd.sock'
check_remote "required remote tools are available" 'command -v curl && command -v jq && command -v base64 && command -v sha256sum'

remote "sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box init --path '$MAIN_BOX' --profile main | jq -e '.status_after.state == \"ok\" and .status_after.dropzone_transfers.runtime_state == \"inactive\"' >/dev/null"
pass "main Box remains inactive for local Dropzone upload"

remote "RUN_ID='$RUN_ID' MAIN_BOX='$MAIN_BOX' bash -s" <<'REMOTE'
set -euo pipefail

TMP_DIR="$(mktemp -d /tmp/loom-v0-4-2-slice-05.XXXXXX)"
chmod 777 "$TMP_DIR"
bad_session_id=""
abort_session_id=""

cleanup() {
  sudo rm -rf "$TMP_DIR" || true
  if [[ -n "${bad_session_id:-}" ]]; then
    sudo rm -rf "$MAIN_BOX/.loom/state/dropzone/incoming/$bad_session_id" || true
  fi
  if [[ -n "${abort_session_id:-}" ]]; then
    sudo rm -rf "$MAIN_BOX/.loom/state/dropzone/incoming/$abort_session_id" || true
  fi
}
trap cleanup EXIT

api() {
  curl --fail --silent --show-error \
    --unix-socket /run/loom/loomd.sock \
    -H 'Content-Type: application/json' \
    -H "X-LOOM-Correlation-ID: corr_${RUN_ID}" \
    "$@"
}

api_status() {
  local output="$1"
  shift
  curl --silent --show-error \
    --unix-socket /run/loom/loomd.sock \
    -H 'Content-Type: application/json' \
    -H "X-LOOM-Correlation-ID: corr_${RUN_ID}" \
    -o "$output" \
    -w '%{http_code}' \
    "$@"
}

post_chunk() {
  local session_id="$1"
  local payload="$2"
  local chunk_size="$3"
  local index="$4"
  local offset=$((index * chunk_size))
  local chunk_b64
  local body

  chunk_b64="$(dd if="$payload" bs=1 skip="$offset" count="$chunk_size" status=none | base64 -w0)"
  body="$(jq -n \
    --argjson index "$index" \
    --argjson offset "$offset" \
    --arg bytes "$chunk_b64" \
    '{chunk_index: $index, offset: $offset, bytes: $bytes}')"
  printf '%s' "$body" | api --data-binary @- "http://loom/v1/box/dropzone/upload-sessions/$session_id/chunks"
}

payload="$TMP_DIR/payload.txt"
printf 'LOOM Dropzone hardening payload %s\n' "$RUN_ID" > "$payload"
size="$(wc -c < "$payload" | tr -d '[:space:]')"
checksum="$(sha256sum "$payload" | awk '{print $1}')"
chunk_size=9
total_chunks=$(((size + chunk_size - 1) / chunk_size))

bad_request="$(jq -n \
  --arg transfer_id "hardening_bad_${RUN_ID//[^A-Za-z0-9_-]/_}" \
  --arg checksum '0000000000000000000000000000000000000000000000000000000000000000' \
  --argjson size "$size" \
  --argjson chunk_size "$chunk_size" \
  '{
    transfer_id: $transfer_id,
    source_node_key: "hardening-workspace",
    source_box_id: "box_hardening_workspace",
    relative_dropzone_path: "bad-checksum.txt",
    file_name: "bad-checksum.txt",
    file_size_bytes: $size,
    checksum_algorithm: "sha256",
    checksum_value: $checksum,
    chunk_size_bytes: $chunk_size
  }')"
printf '%s' "$bad_request" | api --data-binary @- http://loom/v1/box/dropzone/upload-sessions > "$TMP_DIR/bad-session.json"
bad_session_id="$(jq -r '.data.session_id' "$TMP_DIR/bad-session.json")"
bad_final_path="$(jq -r '.data.final_custody_path' "$TMP_DIR/bad-session.json")"
for ((index = 0; index < total_chunks; index++)); do
  post_chunk "$bad_session_id" "$payload" "$chunk_size" "$index" > "$TMP_DIR/bad-chunk-$index.json"
done
bad_code="$(api_status "$TMP_DIR/bad-complete.json" -X POST "http://loom/v1/box/dropzone/upload-sessions/$bad_session_id/complete")"
[[ "$bad_code" == "400" ]] || { cat "$TMP_DIR/bad-complete.json" >&2; exit 1; }
api "http://loom/v1/box/dropzone/upload-sessions/$bad_session_id" > "$TMP_DIR/bad-inspect.json"
jq -e '.ok == true and .data.status == "failed" and .data.failure_code == "dropzone.checksum_mismatch"' "$TMP_DIR/bad-inspect.json" >/dev/null
if sudo -u loom test -e "$bad_final_path"; then
  printf 'bad checksum exposed final custody path: %s\n' "$bad_final_path" >&2
  exit 1
fi

abort_request="$(jq -n \
  --arg transfer_id "hardening_abort_${RUN_ID//[^A-Za-z0-9_-]/_}" \
  --arg checksum "$checksum" \
  --argjson size "$size" \
  --argjson chunk_size "$chunk_size" \
  '{
    transfer_id: $transfer_id,
    source_node_key: "hardening-workspace",
    source_box_id: "box_hardening_workspace",
    relative_dropzone_path: "abort-cleanup.txt",
    file_name: "abort-cleanup.txt",
    file_size_bytes: $size,
    checksum_algorithm: "sha256",
    checksum_value: $checksum,
    chunk_size_bytes: $chunk_size
  }')"
printf '%s' "$abort_request" | api --data-binary @- http://loom/v1/box/dropzone/upload-sessions > "$TMP_DIR/abort-session.json"
abort_session_id="$(jq -r '.data.session_id' "$TMP_DIR/abort-session.json")"
abort_chunks_dir="$(jq -r '.data.chunks_dir' "$TMP_DIR/abort-session.json")"
post_chunk "$abort_session_id" "$payload" "$chunk_size" 0 > "$TMP_DIR/abort-chunk.json"
sudo -u loom test -d "$abort_chunks_dir"
printf '{"reason":"slice 05 hardening abort"}' | api --data-binary @- "http://loom/v1/box/dropzone/upload-sessions/$abort_session_id/abort" > "$TMP_DIR/abort.json"
jq -e '.ok == true and .data.status == "aborted" and .data.failure_code == "dropzone.aborted"' "$TMP_DIR/abort.json" >/dev/null
if sudo -u loom test -d "$abort_chunks_dir"; then
  printf 'abort left temporary chunks behind: %s\n' "$abort_chunks_dir" >&2
  exit 1
fi

sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box dropzone status --path "$MAIN_BOX" --profile main > "$TMP_DIR/main-dropzone-status.json"
jq -e '.runtime_state == "inactive" and .policy_enabled == false' "$TMP_DIR/main-dropzone-status.json" >/dev/null
grep -q 'safe_to_delete' "$MAIN_BOX/Dropzone/README.md"

CLEAN_BOX="$TMP_DIR/Clean Box"
sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box init --path "$CLEAN_BOX" --profile workspace > "$TMP_DIR/clean-box.json"
jq -e '.status_after.state == "ok" and .status_after.dropzone_transfers.runtime_state == "active"' "$TMP_DIR/clean-box.json" >/dev/null
box_id="$(jq -r '.status_after.contract.box_id' "$TMP_DIR/clean-box.json")"
old_time="$(date -u -d '8 days ago' +%Y-%m-%dT%H:%M:%SZ)"
recent_time="$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)"
sudo -u loom mkdir -p "$CLEAN_BOX/Dropzone" "$CLEAN_BOX/.loom/state/dropzone/transfers"
printf 'failed local should remain\n' | sudo -u loom tee "$CLEAN_BOX/Dropzone/failed-old.bin" >/dev/null
failed_size="$(sudo -u loom stat -c %s "$CLEAN_BOX/Dropzone/failed-old.bin")"
jq -n \
  --arg schema 'loom.dropzone.transfer.v0.4.2' \
  --arg box_id "$box_id" \
  --arg root "$CLEAN_BOX" \
  --arg local_path "$CLEAN_BOX/Dropzone/failed-old.bin" \
  --arg old "$old_time" \
  --arg recent "$recent_time" \
  --argjson failed_size "$failed_size" \
  '[
    {
      schema_version: $schema,
      transfer_id: "drop_failed_old",
      source_node_key: "hardening-clean",
      source_box_id: $box_id,
      source_box_root_path: $root,
      local_absolute_path: $local_path,
      relative_dropzone_path: "failed-old.bin",
      file_name: "failed-old.bin",
      file_kind: "file",
      file_size_bytes: $failed_size,
      mod_time: $old,
      checksum_algorithm: "sha256",
      chunk_size_bytes: 9,
      total_chunks: 1,
      status: "failed",
      failure_message: "old failed record",
      updated_at: $old,
      safe_to_delete: false
    },
    {
      schema_version: $schema,
      transfer_id: "drop_abandoned_old",
      source_node_key: "hardening-clean",
      source_box_id: $box_id,
      source_box_root_path: $root,
      local_absolute_path: ($root + "/Dropzone/missing.bin"),
      relative_dropzone_path: "missing.bin",
      file_name: "missing.bin",
      file_kind: "file",
      file_size_bytes: 1,
      mod_time: $old,
      checksum_algorithm: "sha256",
      chunk_size_bytes: 9,
      total_chunks: 1,
      status: "abandoned",
      failure_message: "old abandoned record",
      updated_at: $old,
      safe_to_delete: false
    },
    {
      schema_version: $schema,
      transfer_id: "drop_failed_recent",
      source_node_key: "hardening-clean",
      source_box_id: $box_id,
      source_box_root_path: $root,
      local_absolute_path: ($root + "/Dropzone/recent.bin"),
      relative_dropzone_path: "recent.bin",
      file_name: "recent.bin",
      file_kind: "file",
      file_size_bytes: 1,
      mod_time: $recent,
      checksum_algorithm: "sha256",
      chunk_size_bytes: 9,
      total_chunks: 1,
      status: "failed",
      failure_message: "recent failed record",
      updated_at: $recent,
      safe_to_delete: false
    }
  ]' > "$TMP_DIR/clean-records.json"
jq -c '.[]' "$TMP_DIR/clean-records.json" | while read -r record; do
  transfer_id="$(printf '%s' "$record" | jq -r '.transfer_id')"
  printf '%s\n' "$record" | sudo -u loom tee "$CLEAN_BOX/.loom/state/dropzone/transfers/$transfer_id.json" >/dev/null
done
sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box dropzone clean --failed --abandoned --older-than 7d --path "$CLEAN_BOX" --profile workspace > "$TMP_DIR/local-clean.json"
jq -e '.removed_count == 2 and .skipped_count == 1 and ([.statuses[]] | index("failed") and index("abandoned"))' "$TMP_DIR/local-clean.json" >/dev/null
sudo -u loom test -f "$CLEAN_BOX/Dropzone/failed-old.bin"
if sudo -u loom test -e "$CLEAN_BOX/.loom/state/dropzone/transfers/drop_failed_old.json"; then
  printf 'old failed transfer record was not cleaned\n' >&2
  exit 1
fi
sudo -u loom test -f "$CLEAN_BOX/.loom/state/dropzone/transfers/drop_failed_recent.json"

printf 'dropzone hardening recovery smoke completed for %s\n' "$RUN_ID"
REMOTE
pass "Dropzone recovery paths fail safely and clean temporary upload payloads"

remote "sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box status --path '$MAIN_BOX' --profile main | jq -e '.state == \"ok\" and .dropzone_transfers.runtime_state == \"inactive\"' >/dev/null"
pass "Box status remains healthy after Dropzone hardening checks"

log "completed $pass_count checks"
