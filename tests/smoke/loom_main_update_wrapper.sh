#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

fail() {
  printf 'loom-main update wrapper smoke: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local value="$2"
  grep -F -- "$value" "$file" >/dev/null || fail "missing expected value: $value"
}

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/loom-main-update-wrapper.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

FAKE_BIN="$TMP_DIR/bin"
SSH_LOG="$TMP_DIR/ssh.log"
RSYNC_LOG="$TMP_DIR/rsync.log"
mkdir -p "$FAKE_BIN"

cat >"$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${LOOM_WRAPPER_SMOKE_SSH_LOG:?}"
{
  printf 'ssh'
  printf ' <%s>' "$@"
  printf '\n'
} >>"$LOOM_WRAPPER_SMOKE_SSH_LOG"

if [[ " $* " == *" sudo -n true "* && "${LOOM_WRAPPER_SMOKE_REQUIRE_PASSWORD:-0}" == "1" ]]; then
  exit 1
fi
if [[ " $* " == *" update apply --help "* ]]; then
  printf '%s\n' "${LOOM_WRAPPER_SMOKE_HELP_OUTPUT:-remote apply help}"
  exit "${LOOM_WRAPPER_SMOKE_HELP_STATUS:-0}"
fi
if [[ " $* " == *" -tt "* && "${LOOM_WRAPPER_SMOKE_REFUSE_APPLY:-0}" == "1" ]]; then
  exit 77
fi
if [[ " $* " == *" backup create "* ]]; then
  if [[ "${LOOM_WRAPPER_SMOKE_OMIT_BACKUP_PATH:-0}" == "1" ]]; then
    printf '{"data":{"run":{"result_summary_json":{"backup_operation_id":"%s"}}}}\n' \
      "${LOOM_WRAPPER_SMOKE_AUTO_BACKUP_REF:-maintenance_operation_fake}"
  elif [[ "${LOOM_WRAPPER_SMOKE_OMIT_BACKUP_REF:-0}" == "1" ]]; then
    printf '{"data":{"run":{"result_summary_json":{"backup_dir":"%s"}}}}\n' \
      "${LOOM_WRAPPER_SMOKE_AUTO_BACKUP:-/var/lib/loom/backups/main/fake}"
  else
    printf '{"data":{"run":{"result_summary_json":{"backup_dir":"%s","backup_operation_id":"%s"}}}}\n' \
      "${LOOM_WRAPPER_SMOKE_AUTO_BACKUP:-/var/lib/loom/backups/main/fake}" \
      "${LOOM_WRAPPER_SMOKE_AUTO_BACKUP_REF:-maintenance_operation_fake}"
  fi
fi
SH
chmod +x "$FAKE_BIN/ssh"
cat >"$FAKE_BIN/security" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'fake-keychain-password\n'
SH
chmod +x "$FAKE_BIN/security"
cat >"$FAKE_BIN/rsync" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${LOOM_WRAPPER_SMOKE_RSYNC_LOG:?}"
{
  printf 'rsync'
  printf ' <%s>' "$@"
  printf '\n'
} >>"$LOOM_WRAPPER_SMOKE_RSYNC_LOG"
SH
chmod +x "$FAKE_BIN/rsync"

export PATH="$FAKE_BIN:$PATH"
export LOOM_MAIN_HOST="acceptance-main"
export LOOM_MAIN_USE_KEYCHAIN_SUDO=0
export LOOM_WRAPPER_SMOKE_SSH_LOG="$SSH_LOG"
export LOOM_WRAPPER_SMOKE_RSYNC_LOG="$RSYNC_LOG"

RELEASE_PATH="/srv/loom/releases/canonical-slice-11-6"
BACKUP_PATH="/var/lib/loom/backups/main/accepted-v09"
BACKUP_REF="maintenance_operation_accepted_v09"

export LOOM_WRAPPER_SMOKE_HELP_OUTPUT="remote apply help is read-only"
export LOOM_WRAPPER_SMOKE_HELP_STATUS=73
for help_arg in -h --help help; do
  : >"$SSH_LOG"
  : >"$RSYNC_LOG"
  set +e
  HELP_OUTPUT="$({
    LOOM_MAIN_UPDATE_SOURCE="$TMP_DIR/missing release source" \
      LOOM_MAIN_UPDATE_RELEASE_PATH= \
      "$REPO_ROOT/scripts/loom-main" update apply "$help_arg"
  } 2>&1)"
  HELP_STATUS=$?
  set -e
  [[ "$HELP_STATUS" == "$LOOM_WRAPPER_SMOKE_HELP_STATUS" ]] || fail "apply $help_arg did not preserve remote help status: $HELP_STATUS"
  [[ "$HELP_OUTPUT" == "$LOOM_WRAPPER_SMOKE_HELP_OUTPUT" ]] || fail "apply $help_arg did not preserve exact remote help output: $HELP_OUTPUT"
  [[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "1" ]] || fail "apply $help_arg should make exactly one read-only SSH call"
  assert_contains "$SSH_LOG" '<-o> <BatchMode=yes> <acceptance-main> <loom> <--config> </etc/loom/loom.env> <update> <apply> <--help>'
  [[ ! -s "$RSYNC_LOG" ]] || fail "apply $help_arg synced a release"
  if grep -F -e '<-tt>' -e '<sudo>' -e '<update> <plan>' -e '<backup> <create>' -e '<chmod>' "$SSH_LOG" >/dev/null; then
    fail "apply $help_arg crossed the read-only help boundary"
  fi
done
unset LOOM_WRAPPER_SMOKE_HELP_OUTPUT LOOM_WRAPPER_SMOKE_HELP_STATUS

: >"$SSH_LOG"
"$REPO_ROOT/scripts/loom-main" update plan \
  --release-path "$RELEASE_PATH" \
  --flake .#hardware-main \
  --current-migration 61 >/dev/null

[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "1" ]] || fail "plan should use exactly one SSH call"
assert_contains "$SSH_LOG" '<-o> <BatchMode=yes> <acceptance-main> <loom>'
if grep -F '<sudo>' "$SSH_LOG" >/dev/null; then
  fail "update plan must remain unprivileged"
fi

: >"$SSH_LOG"
"$REPO_ROOT/scripts/loom-main" sudo systemctl status loomd >/dev/null
[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "2" ]] || fail "sudo command should check passwordless sudo before running"
assert_contains "$SSH_LOG" '<-o> <BatchMode=yes> <acceptance-main> <sudo> <-n> <true>'
assert_contains "$SSH_LOG" '<-o> <BatchMode=yes> <acceptance-main> <sudo> <-n> <--> <systemctl> <status> <loomd>'

: >"$SSH_LOG"
export LOOM_WRAPPER_SMOKE_REQUIRE_PASSWORD=1
"$REPO_ROOT/scripts/loom-main" sudo systemctl status loomd >/dev/null
unset LOOM_WRAPPER_SMOKE_REQUIRE_PASSWORD
[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "2" ]] || fail "sudo command should use Keychain only after passwordless sudo is refused"
assert_contains "$SSH_LOG" "<-o> <BatchMode=yes> <acceptance-main> <sudo -S -p '' systemctl status loomd>"

: >"$SSH_LOG"
"$REPO_ROOT/scripts/loom-main" update apply \
  --release-path "$RELEASE_PATH" \
  --backup-path "$BACKUP_PATH" \
  --backup-ref "$BACKUP_REF" \
  --flake .#hardware-main \
  --current-migration 61 \
  --yes \
  --no-pause-schedules \
  --no-pause-direct-events \
  --no-pause-workers >/dev/null

[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "3" ]] || fail "apply should plan once, check passwordless sudo once, and invoke one transaction"
assert_contains "$SSH_LOG" '<-o> <BatchMode=yes> <acceptance-main> <sudo> <-n> <true>'
assert_contains "$SSH_LOG" '<-tt> <acceptance-main> <loom> <--config> </etc/loom/loom.env> <update> <apply>'
assert_contains "$SSH_LOG" "<--release-path> <$RELEASE_PATH>"
assert_contains "$SSH_LOG" "<--backup-path> <$BACKUP_PATH>"
assert_contains "$SSH_LOG" "<--backup-ref> <$BACKUP_REF>"
assert_contains "$SSH_LOG" '<--flake> <.#hardware-main>'
assert_contains "$SSH_LOG" '<--yes>'
assert_contains "$SSH_LOG" '<--no-pause-schedules>'
assert_contains "$SSH_LOG" '<--no-pause-direct-events>'
assert_contains "$SSH_LOG" '<--no-pause-workers>'

SOURCE_ROOT="$TMP_DIR/release source"
mkdir -p "$SOURCE_ROOT"
: >"$SSH_LOG"
: >"$RSYNC_LOG"
LOOM_MAIN_UPDATE_SOURCE="$SOURCE_ROOT" \
  "$REPO_ROOT/scripts/loom-main" update apply \
    --release-path "$RELEASE_PATH" \
    --backup-path "$BACKUP_PATH" \
    --backup-ref "$BACKUP_REF" \
    --flake .#hardware-main \
    --current-migration 61 \
    --yes >/dev/null
[[ "$(wc -l <"$RSYNC_LOG" | tr -d ' ')" == "1" ]] || fail "release source should be synced once"
assert_contains "$RSYNC_LOG" "<$SOURCE_ROOT/>"
assert_contains "$RSYNC_LOG" "<acceptance-main:$RELEASE_PATH/>"
[[ "$(grep -c '<chmod> <0755>' "$SSH_LOG" || true)" == "1" ]] || fail "synced release root should be normalized once"
assert_contains "$SSH_LOG" "<chmod> <0755> <$RELEASE_PATH>"
[[ "$(grep -c '<update> <plan>' "$SSH_LOG" || true)" == "1" ]] || fail "synced update should be planned once"
[[ "$(grep -c '<update> <apply>' "$SSH_LOG" || true)" == "1" ]] || fail "synced update should be applied once"

AUTO_BACKUP="/var/lib/loom/backups/main/auto-v09"
AUTO_BACKUP_REF="maintenance_operation_auto_v09"
export LOOM_WRAPPER_SMOKE_AUTO_BACKUP="$AUTO_BACKUP"
export LOOM_WRAPPER_SMOKE_AUTO_BACKUP_REF="$AUTO_BACKUP_REF"
: >"$SSH_LOG"
"$REPO_ROOT/scripts/loom-main" update apply \
  --release-path "$RELEASE_PATH" \
  --flake .#hardware-main \
  --current-migration 61 \
  --yes >/dev/null
[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "4" ]] || fail "automatic backup apply should plan, back up, check passwordless sudo, and apply once"
assert_contains "$SSH_LOG" '<loom> <--config> </etc/loom/loom.env> <--json> <backup> <create> <--production>'
assert_contains "$SSH_LOG" "<--reason> <pre-update-backup-for:$RELEASE_PATH>"
assert_contains "$SSH_LOG" "<--backup-path> <$AUTO_BACKUP>"
assert_contains "$SSH_LOG" "<--backup-ref> <$AUTO_BACKUP_REF>"

for missing_field in PATH REF; do
  before_apply_calls="$(grep -c '<update> <apply>' "$SSH_LOG" || true)"
  export "LOOM_WRAPPER_SMOKE_OMIT_BACKUP_${missing_field}=1"
  if "$REPO_ROOT/scripts/loom-main" update apply \
    --release-path "$RELEASE_PATH" \
    --flake .#hardware-main \
    --current-migration 61 \
    --yes >/dev/null 2>&1; then
    fail "automatic backup without $missing_field evidence was accepted"
  fi
  unset "LOOM_WRAPPER_SMOKE_OMIT_BACKUP_${missing_field}"
  after_apply_calls="$(grep -c '<update> <apply>' "$SSH_LOG" || true)"
  [[ "$after_apply_calls" == "$before_apply_calls" ]] || fail "automatic backup missing $missing_field reached apply"
done

BEFORE_MISSING_REF_CALLS="$(wc -l <"$SSH_LOG" | tr -d ' ')"
if "$REPO_ROOT/scripts/loom-main" update apply \
  --release-path "$RELEASE_PATH" \
  --backup-path "$BACKUP_PATH" \
  --current-migration 61 \
  --yes >/dev/null 2>&1; then
  fail "operator-supplied backup path without backup ref was accepted"
fi
[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "$BEFORE_MISSING_REF_CALLS" ]] || fail "missing backup ref reached SSH"

BEFORE_CALLS="$(wc -l <"$SSH_LOG" | tr -d ' ')"
SENTINEL="$TMP_DIR/should-not-exist"
if "$REPO_ROOT/scripts/loom-main" update apply \
  --release-path "$RELEASE_PATH;touch $SENTINEL" \
  --backup-path "$BACKUP_PATH" \
  --backup-ref "$BACKUP_REF" \
  --current-migration 61 \
  --yes >/dev/null 2>&1; then
  fail "unsafe remote argument was accepted"
fi
[[ ! -e "$SENTINEL" ]] || fail "unsafe argument executed a second command"
[[ "$(wc -l <"$SSH_LOG" | tr -d ' ')" == "$BEFORE_CALLS" ]] || fail "unsafe argument reached SSH"

export LOOM_WRAPPER_SMOKE_REFUSE_APPLY=1
set +e
"$REPO_ROOT/scripts/loom-main" update apply \
  --release-path "$RELEASE_PATH" \
  --backup-path "$BACKUP_PATH" \
  --backup-ref "$BACKUP_REF" \
  --current-migration 61 \
  --yes >/dev/null 2>&1
REFUSED_RC=$?
set -e
[[ "$REFUSED_RC" == "77" ]] || fail "remote apply refusal exit status was not preserved: $REFUSED_RC"

if command -v expect >/dev/null 2>&1; then
  export LOOM_MAIN_USE_KEYCHAIN_SUDO=1
  export LOOM_WRAPPER_SMOKE_REQUIRE_PASSWORD=1
  set +e
  "$REPO_ROOT/scripts/loom-main" update apply \
    --release-path "$RELEASE_PATH" \
    --backup-path "$BACKUP_PATH" \
    --backup-ref "$BACKUP_REF" \
    --current-migration 61 \
    --yes >/dev/null 2>&1
  KEYCHAIN_REFUSED_RC=$?
  set -e
  [[ "$KEYCHAIN_REFUSED_RC" == "77" ]] || fail "Keychain/Expect remote exit status was not preserved: $KEYCHAIN_REFUSED_RC"
  unset LOOM_WRAPPER_SMOKE_REQUIRE_PASSWORD
fi

printf '[smoke] loom-main update wrapper smoke passed\n'
