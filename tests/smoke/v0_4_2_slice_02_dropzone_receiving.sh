#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
RUN_ID="v0-4-2-slice-02-$(date +%s)-$RANDOM"
MAIN_BOX="/var/lib/loom/LOOM Box"
pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
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
log "run id: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loomd Unix socket exists" 'test -S /run/loom/loomd.sock'
check_remote "required remote tools are available" 'command -v curl && command -v jq && command -v base64 && command -v sha256sum'

check_json "main Box scaffold exists for daemon receiving root" \
  "sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box init --path '$MAIN_BOX' --profile main" \
  ".profile == \"main\" and .status_after.state == \"ok\""

remote "RUN_ID='$RUN_ID' MAIN_BOX='$MAIN_BOX' bash -s" <<'REMOTE'
set -euo pipefail

TMP_DIR="$(mktemp -d /tmp/loom-v0-4-2-slice-02.XXXXXX)"
session_id=""
abort_id=""
final_path=""

cleanup() {
  rm -rf "$TMP_DIR" || true
  if [[ -n "${session_id:-}" ]]; then
    sudo rm -rf "$MAIN_BOX/.loom/state/dropzone/incoming/$session_id" || true
  fi
  if [[ -n "${abort_id:-}" ]]; then
    sudo rm -rf "$MAIN_BOX/.loom/state/dropzone/incoming/$abort_id" || true
  fi
  if [[ -n "${final_path:-}" ]]; then
    sudo rm -rf "$(dirname "$final_path")" || true
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

payload="$TMP_DIR/payload.txt"
printf 'LOOM Dropzone Slice 02 payload for %s\n' "$RUN_ID" > "$payload"
size="$(wc -c < "$payload" | tr -d '[:space:]')"
checksum="$(sha256sum "$payload" | awk '{print $1}')"
chunk_size=11
transfer_id="smoke_${RUN_ID//[^A-Za-z0-9_-]/_}"

session_request="$(jq -n \
  --arg transfer_id "$transfer_id" \
  --arg checksum "$checksum" \
  --argjson size "$size" \
  --argjson chunk_size "$chunk_size" \
  '{
    transfer_id: $transfer_id,
    source_node_key: "smoke-workspace",
    source_box_id: "box_smoke_workspace",
    relative_dropzone_path: "payload.txt",
    file_name: "payload.txt",
    file_size_bytes: $size,
    checksum_algorithm: "sha256",
    checksum_value: $checksum,
    chunk_size_bytes: $chunk_size
  }')"

printf '%s' "$session_request" | api --data-binary @- http://loom/v1/box/dropzone/upload-sessions > "$TMP_DIR/session.json"
jq -e --arg transfer_id "$transfer_id" '.ok == true and .data.transfer_id == $transfer_id and .data.status == "receiving" and .data.total_chunks > 1' "$TMP_DIR/session.json" >/dev/null
session_id="$(jq -r '.data.session_id' "$TMP_DIR/session.json")"

post_chunk() {
  local index="$1"
  local offset=$((index * chunk_size))
  local body
  local chunk_b64

  chunk_b64="$(dd if="$payload" bs=1 skip="$offset" count="$chunk_size" status=none | base64 -w0)"
  body="$(jq -n \
    --argjson index "$index" \
    --argjson offset "$offset" \
    --arg bytes "$chunk_b64" \
    '{chunk_index: $index, offset: $offset, bytes: $bytes}')"
  printf '%s' "$body" | api --data-binary @- "http://loom/v1/box/dropzone/upload-sessions/$session_id/chunks"
}

post_chunk 0 > "$TMP_DIR/chunk-0.json"
jq -e '.ok == true and .data.chunk_index == 0 and .data.idempotent == false and .data.session.accepted_bytes > 0' "$TMP_DIR/chunk-0.json" >/dev/null

post_chunk 0 > "$TMP_DIR/chunk-0-duplicate.json"
jq -e '.ok == true and .data.chunk_index == 0 and .data.idempotent == true' "$TMP_DIR/chunk-0-duplicate.json" >/dev/null

total_chunks="$(jq -r '.data.total_chunks' "$TMP_DIR/session.json")"
for ((index = 1; index < total_chunks; index++)); do
  post_chunk "$index" > "$TMP_DIR/chunk-$index.json"
  jq -e --argjson index "$index" '.ok == true and .data.chunk_index == $index' "$TMP_DIR/chunk-$index.json" >/dev/null
done

api -X POST "http://loom/v1/box/dropzone/upload-sessions/$session_id/complete" > "$TMP_DIR/complete.json"
jq -e --arg checksum "$checksum" '.ok == true and .data.safe_to_delete == true and .data.checksum_value == $checksum and .data.session.status == "accepted"' "$TMP_DIR/complete.json" >/dev/null

final_path="$(jq -r '.data.final_path' "$TMP_DIR/complete.json")"
metadata_path="$(jq -r '.data.custody_metadata_path' "$TMP_DIR/complete.json")"
sudo -u loom test -f "$final_path"
sudo -u loom test -f "$metadata_path"
sudo -u loom sha256sum "$final_path" | awk '{print $1}' | grep -qx "$checksum"
jq -e --arg session_id "$session_id" --arg checksum "$checksum" '.schema_version == "loom.dropzone.custody.v0.4.2" and .session_id == $session_id and .checksum_value == $checksum' "$metadata_path" >/dev/null

api "http://loom/v1/box/dropzone/upload-sessions/$session_id" > "$TMP_DIR/inspect.json"
jq -e '.ok == true and .data.status == "accepted" and .data.custody_id != ""' "$TMP_DIR/inspect.json" >/dev/null

api "http://loom/v1/box/dropzone/upload-sessions?limit=10" > "$TMP_DIR/list.json"
jq -e --arg session_id "$session_id" '.ok == true and ([.data[].session_id] | index($session_id) != null)' "$TMP_DIR/list.json" >/dev/null

api -X POST "http://loom/v1/box/dropzone/upload-sessions/$session_id/complete" > "$TMP_DIR/complete-again.json"
jq -e --arg final_path "$final_path" '.ok == true and .data.safe_to_delete == true and .data.final_path == $final_path' "$TMP_DIR/complete-again.json" >/dev/null

abort_request="$(jq -n \
  --arg transfer_id "${transfer_id}_abort" \
  --arg checksum "$checksum" \
  --argjson size "$size" \
  --argjson chunk_size "$chunk_size" \
  '{
    transfer_id: $transfer_id,
    source_node_key: "smoke-workspace",
    source_box_id: "box_smoke_workspace",
    relative_dropzone_path: "payload-abort.txt",
    file_name: "payload-abort.txt",
    file_size_bytes: $size,
    checksum_algorithm: "sha256",
    checksum_value: $checksum,
    chunk_size_bytes: $chunk_size
  }')"
printf '%s' "$abort_request" | api --data-binary @- http://loom/v1/box/dropzone/upload-sessions > "$TMP_DIR/abort-session.json"
abort_id="$(jq -r '.data.session_id' "$TMP_DIR/abort-session.json")"
printf '{"reason":"slice 02 smoke abort"}' | api --data-binary @- "http://loom/v1/box/dropzone/upload-sessions/$abort_id/abort" > "$TMP_DIR/abort.json"
jq -e '.ok == true and .data.status == "aborted" and .data.failure_code == "dropzone.aborted"' "$TMP_DIR/abort.json" >/dev/null

printf 'dropzone receiving API smoke completed for %s\n' "$session_id"
REMOTE
pass "Dropzone receiving API accepts chunks, verifies custody, and exposes sessions"

log "completed $pass_count checks"
