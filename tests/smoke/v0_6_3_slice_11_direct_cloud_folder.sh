#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

assert_contains() {
  local haystack="$1"
  local needle="$2"
  if [[ "$haystack" != *"$needle"* ]]; then
    printf 'expected output to contain: %s\n' "$needle" >&2
    printf 'actual output:\n%s\n' "$haystack" >&2
    exit 1
  fi
}

bash -n \
  scripts/loom-macbook \
  scripts/raycast/loom-cloud-mount.sh \
  scripts/raycast/loom-cloud-status.sh \
  scripts/raycast/loom-cloud-unmount.sh

help_output="$(scripts/loom-macbook help)"
assert_contains "$help_output" "cloud-status"
assert_contains "$help_output" "cloud-enable"
assert_contains "$help_output" "cloud-disable"
assert_contains "$help_output" "cloud-repair-once"
assert_contains "$help_output" "mount-cloud"
assert_contains "$help_output" "unmount-cloud"
assert_contains "$help_output" "restart-cloud"
assert_contains "$help_output" "LOOM_CLOUD_FOLDER_SMB_SHARE"

default_status="$(
  LOOM_CLOUD_FOLDER_MOUNT="$TMP_DIR/LOOM Cloud Default" \
  LOOM_CLOUD_FOLDER_SMB_HOST=127.0.0.1 \
  LOOM_CLOUD_FOLDER_SMB_SHARE=backup \
  LOOM_CLOUD_FOLDER_SMB_USER=loom-cloud-test \
  scripts/loom-macbook cloud-status
)"
assert_contains "$default_status" "protocol: smb"
assert_contains "$default_status" "scope: trusted admin direct Storage Box SMB mount"
assert_contains "$default_status" "managed roots exposed: yes, intentionally"

rclone_status="$(
  LOOM_CLOUD_FOLDER_PROTOCOL=rclone \
  LOOM_CLOUD_FOLDER_MOUNT="$TMP_DIR/LOOM Cloud" \
  LOOM_CLOUD_FOLDER_RCLONE_CONFIG="$TMP_DIR/loom-cloud-folder.conf" \
  LOOM_CLOUD_STORAGE_KEY="$TMP_DIR/missing-storage-box-key" \
  scripts/loom-macbook cloud-status
)"
assert_contains "$rclone_status" "protocol: rclone"
assert_contains "$rclone_status" "remote: loom-cloud-folder:loom/cloud-folder"
assert_contains "$rclone_status" "managed roots exposed: no"
assert_contains "$rclone_status" "scope: user-managed direct cloud folder only"
assert_contains "$rclone_status" "rclone binary:"

smb_status="$(
  LOOM_CLOUD_FOLDER_PROTOCOL=smb \
  LOOM_CLOUD_FOLDER_MOUNT="$TMP_DIR/LOOM Cloud SMB" \
  LOOM_CLOUD_FOLDER_SMB_HOST=127.0.0.1 \
  LOOM_CLOUD_FOLDER_SMB_SHARE=backup \
  LOOM_CLOUD_FOLDER_SMB_USER=loom-cloud-test \
  scripts/loom-macbook cloud-status
)"
assert_contains "$smb_status" "protocol: smb"
assert_contains "$smb_status" "finder url: smb://loom-cloud-test@127.0.0.1/backup"
assert_contains "$smb_status" "managed roots exposed: yes, intentionally"

printf 'v0.6.3 slice 11 direct cloud folder smoke passed\n'
