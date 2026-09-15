#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PACK="$ROOT/internal/projectcontracts/repo_development_pack"
TMP_BASE="${TMPDIR:-/tmp}"
TMP_BASE="${TMP_BASE%/}"
TMP_ROOT="$(mktemp -d "$TMP_BASE/loom-repo-development-state.XXXXXX")"
TMP_ROOT="$(cd "$TMP_ROOT" && pwd -P)"
LOOM_BIN="$TMP_ROOT/bin/loom"
REPOSITORY_ID='repo_01ARZ3NDEKTSV4RRFFQ69G5FAV'
PROJECT_ID='project_01ARZ3NDEKTSV4RRFFQ69G5FAV'
PROJECT_SLUG='atlas'
PASS_COUNT=0

log() {
  printf '[repo-development-state] %s\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM HUP
  set +e
  case "$TMP_ROOT" in
    */loom-repo-development-state.*)
      find "$TMP_ROOT" -xdev -depth -delete >/dev/null 2>&1 || true
      ;;
  esac
  exit "$exit_status"
}
trap cleanup EXIT INT TERM HUP

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

assert_json() {
  local path="$1"
  local expression="$2"
  local message="$3"
  jq -e "$expression" "$path" >/dev/null || fail "$message: $path"
}

assert_equal() {
  local got="$1"
  local want="$2"
  local message="$3"
  [[ "$got" == "$want" ]] || fail "$message: got '$got', want '$want'"
}

assert_file_absent() {
  local path="$1"
  [[ ! -e "$path" ]] || fail "unexpected path exists: $path"
}

assert_file_present() {
  local path="$1"
  [[ -f "$path" ]] || fail "expected file is absent: $path"
}

init_git_repository() {
  local repository_root="$1"
  mkdir -p "$repository_root"
  git -C "$repository_root" init -q -b main
  printf '# Disposable repository\n' >"$repository_root/README.md"
  git -C "$repository_root" add README.md
  git -C "$repository_root" \
    -c user.name='LOOM Slice 5' \
    -c user.email='slice5@invalid' \
    commit -q -m 'fixture: initialize repository'
}

commit_fixture() {
  local repository_root="$1"
  local message="$2"
  shift 2
  git -C "$repository_root" add -- "$@"
  git -C "$repository_root" \
    -c user.name='LOOM Slice 5' \
    -c user.email='slice5@invalid' \
    commit -q -m "$message"
}

clone_fixture() {
  local source_root="$1"
  local destination_root="$2"
  git clone -q --no-local "$source_root" "$destination_root"
}

validate_repository() {
  local repository_root="$1"
  local output="$2"
  "$LOOM_BIN" --json repo-state validate \
    --path "$repository_root" \
    --repository-id "$REPOSITORY_ID" \
    --owner-project-id "$PROJECT_ID" \
    --owner-project-slug "$PROJECT_SLUG" \
    --role component >"$output"
}

plan_initialization() {
  local repository_root="$1"
  local output="$2"
  "$LOOM_BIN" --json repo-state init \
    --path "$repository_root" \
    --pack "$PACK" \
    --repository-id "$REPOSITORY_ID" \
    --repository-name 'Atlas API' \
    --alias atlas \
    --alias atlas-api \
    --role component \
    --purpose 'Serve the disposable Atlas API fixture.' \
    --topic api \
    --topic atlas \
    --topic go \
    --owner-project-id "$PROJECT_ID" \
    --owner-project-slug "$PROJECT_SLUG" \
    --stable-branch main \
    --default-branch main >"$output"
}

apply_initialization() {
  local repository_root="$1"
  local digest="$2"
  local output="$3"
  "$LOOM_BIN" --json repo-state init \
    --path "$repository_root" \
    --pack "$PACK" \
    --repository-id "$REPOSITORY_ID" \
    --repository-name 'Atlas API' \
    --alias atlas \
    --alias atlas-api \
    --role component \
    --purpose 'Serve the disposable Atlas API fixture.' \
    --topic api \
    --topic atlas \
    --topic go \
    --owner-project-id "$PROJECT_ID" \
    --owner-project-slug "$PROJECT_SLUG" \
    --stable-branch main \
    --default-branch main \
    --apply \
    --plan-digest "$digest" \
    --yes >"$output"
}

absolute_path_token_boundary() {
  local value="${1-}"
  case "$value" in
    ''|' '|$'\t'|$'\r'|$'\n'|'"'|"'"|'`'|'='|':'|'('|\[|'{'|'<'|','|';'|'|')
      return 0
      ;;
  esac
  return 1
}

absolute_path_terminator() {
  local value="${1-}"
  case "$value" in
    ''|' '|$'\t'|$'\r'|$'\n'|'"'|"'"|'`'|'<'|'>'|')'|\]|'}'|','|';'|'|')
      return 0
      ;;
  esac
  return 1
}

has_unc_server_and_share() {
  local remaining="$1"
  local separator="${remaining:0:1}"
  local length="${#remaining}"
  local index=2
  local server_start=2
  local value

  while ((index < length)); do
    value="${remaining:index:1}"
    if [[ "$value" == "$separator" ]] || absolute_path_terminator "$value"; then
      break
    fi
    index=$((index + 1))
  done
  if ((index == server_start || index >= length)) || [[ "${remaining:index:1}" != "$separator" ]]; then
    return 1
  fi
  index=$((index + 1))
  if ((index >= length)); then
    return 1
  fi
  value="${remaining:index:1}"
  [[ "$value" != "$separator" ]] && ! absolute_path_terminator "$value"
}

file_contains_host_absolute_path() {
  local path="$1"
  local line
  local length
  local index
  local previous
  local remaining
  local value

  while IFS= read -r line || [[ -n "$line" ]]; do
    length="${#line}"
    for ((index = 0; index < length; index++)); do
      previous=''
      if ((index > 0)); then
        previous="${line:index-1:1}"
      fi
      absolute_path_token_boundary "$previous" || continue
      remaining="${line:index}"

      case "$remaining" in
        [Hh][Tt][Tt][Pp]://*|[Hh][Tt][Tt][Pp][Ss]://*)
          while ((index < length)) && ! absolute_path_terminator "${line:index:1}"; do
            index=$((index + 1))
          done
          continue
          ;;
        [Ff][Ii][Ll][Ee]:/*|[Gg][Ii][Tt]+[Ff][Ii][Ll][Ee]:/*)
          return 0
          ;;
      esac

      if [[ "${remaining:0:1}" =~ [A-Za-z] ]] \
        && [[ "${remaining:1:1}" == ':' ]] \
        && { [[ "${remaining:2:1}" == '/' ]] || [[ "${remaining:2:1}" == "\\" ]]; }; then
        return 0
      fi

      if [[ "${remaining:0:2}" == '//' || "${remaining:0:2}" == "\\\\" ]]; then
        if has_unc_server_and_share "$remaining"; then
          return 0
        fi
        continue
      fi

      value="${remaining:0:1}"
      if [[ "$value" == '/' ]] && ! absolute_path_terminator "${remaining:1:1}"; then
        return 0
      fi
    done
  done <"$path"
  return 1
}

host_absolute_path_violation() {
  local state_root="$1"
  local candidate
  while IFS= read -r -d '' candidate; do
    if file_contains_host_absolute_path "$candidate"; then
      printf 'host-absolute path found below %s in %s\n' "$state_root" "$candidate"
      return 0
    fi
  done < <(find "$state_root" -type f -print0)
  return 1
}

portable_tree_violation() {
  local repository_root="$1"
  shift
  local state_root="$repository_root/.repo"
  local value
  local candidate

  candidate="$(find "$state_root" ! -type d ! -type f -print -quit)"
  if [[ -n "$candidate" ]]; then
    printf 'non-regular portable path: %s\n' "$candidate"
    return 0
  fi

  while IFS= read -r -d '' candidate; do
    case "$(basename "$candidate" | tr '[:upper:]' '[:lower:]')" in
      .env|.env.*|*.key|*.pem|*.p12|*.log|*.lock|*.sock|*credential*|*secret*)
        printf 'forbidden portable filename: %s\n' "$candidate"
        return 0
        ;;
    esac
  done < <(find "$state_root" -type f -print0)

  while IFS= read -r -d '' candidate; do
    case "$(basename "$candidate" | tr '[:upper:]' '[:lower:]')" in
      .cache|cache|caches|logs|sessions|private-memory|runtime|generated-indexes)
        printf 'forbidden portable directory: %s\n' "$candidate"
        return 0
        ;;
    esac
  done < <(find "$state_root" -type d -print0)

  if host_absolute_path_violation "$state_root"; then
    return 0
  fi

  if grep -R -n -i -E \
    '(api[_-]?key|access[_-]?token|refresh[_-]?token|password|client[_-]?secret)[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9/+_.-]{8,}|(session|thread|terminal|conversation)[_-]?id[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9_.:-]{8,}|(private[_ -]?memory|hidden[_ -]?prompt)[[:space:]]*[:=]' \
    "$state_root" >/dev/null; then
    printf 'credential, runtime identifier, or private-memory assignment found below %s\n' "$state_root"
    return 0
  fi

  for value in \
    'LOOM_SECRET_SENTINEL_SLICE5' \
    'ORCA_SESSION_SENTINEL_SLICE5' \
    'CODEX_SESSION_SENTINEL_SLICE5' \
    'PRIVATE_MEMORY_SENTINEL_SLICE5' \
    'CACHE_SENTINEL_SLICE5' \
    "$TMP_ROOT" \
    "$@"; do
    if grep -R -F -- "$value" "$state_root" >/dev/null; then
      printf 'runtime sentinel leaked below %s: %s\n' "$state_root" "$value"
      return 0
    fi
  done
  return 1
}

assert_portable_tree() {
  local repository_root="$1"
  shift
  local report
  if report="$(portable_tree_violation "$repository_root" "$@")"; then
    fail "$report"
  fi
}

assert_portable_rejection() {
  local repository_root="$1"
  local label="$2"
  local report
  if ! report="$(portable_tree_violation "$repository_root")"; then
    fail "portable scanner accepted forbidden $label fixture"
  fi
}

require_command go
require_command git
require_command jq
require_command sed
require_command grep
require_command find
require_command cmp
require_command tr
require_command mkfifo

mkdir -p "$TMP_ROOT/bin" "$TMP_ROOT/runtime/live" "$TMP_ROOT/runtime/archive"
printf 'LOOM_SECRET_SENTINEL_SLICE5\n' >"$TMP_ROOT/runtime/live/credential"
printf 'ORCA_SESSION_SENTINEL_SLICE5\n' >"$TMP_ROOT/runtime/live/orca-session"
printf 'CODEX_SESSION_SENTINEL_SLICE5\n' >"$TMP_ROOT/runtime/live/codex-session"
printf 'PRIVATE_MEMORY_SENTINEL_SLICE5\n' >"$TMP_ROOT/runtime/live/private-memory"
printf 'CACHE_SENTINEL_SLICE5\n' >"$TMP_ROOT/runtime/live/cache"

log 'building a disposable loom binary'
(cd "$ROOT" && go build -o "$LOOM_BIN" ./cmd/loom)
pass 'real CLI binary built outside the repository'

log 'self-testing portable scanner rejection and URL preservation'
scanner_root="$TMP_ROOT/scanner-self-test"
scanner_state="$scanner_root/.repo/STATE.md"
mkdir -p "$scanner_root/.repo"
scanner_cases=(
  'path: /tmp'
  'path: /srv/loom/state.yaml'
  'worktree: C:\LOOM'
  'worktree: D:/work/loom'
  'share: \\server\share'
  'share: //server/share'
  'source: file:///srv/loom'
  'source: git+file:/srv/loom'
)
for value in "${scanner_cases[@]}"; do
  printf '%s\n' "$value" >"$scanner_state"
  assert_portable_rejection "$scanner_root" "$value"
done
printf '%s\n' \
  'url: https://example.com/tmp' \
  '[reference](HTTP://example.com/C:/work/loom)' \
  >"$scanner_state"
assert_portable_tree "$scanner_root"
ln -s "$TMP_ROOT/runtime/live/credential" "$scanner_root/.repo/runtime-link"
assert_portable_rejection "$scanner_root" 'symlink'
rm "$scanner_root/.repo/runtime-link"
mkfifo "$scanner_root/.repo/runtime-pipe"
assert_portable_rejection "$scanner_root" 'FIFO'
rm "$scanner_root/.repo/runtime-pipe"
assert_portable_tree "$scanner_root"
pass 'scanner rejects POSIX, drive, UNC, file-URI, symlink, and FIFO fixtures while preserving HTTP(S) URLs'

log 'validating an absent repository state pack'
absent_root="$TMP_ROOT/absent-pack"
init_git_repository "$absent_root"
validate_repository "$absent_root" "$TMP_ROOT/absent.json"
assert_json "$TMP_ROOT/absent.json" \
  '.tracking_status == "not_enabled" and .freshness.tracked_state == "unavailable" and (.accepted_context | length) == 0' \
  'absent pack did not remain explicitly disabled'
assert_file_absent "$absent_root/.repo"
pass 'absent pack is read-only not_enabled state'

log 'planning and explicitly applying the optional project-local pack'
current_root="$TMP_ROOT/current-pack"
init_git_repository "$current_root"
plan_initialization "$current_root" "$TMP_ROOT/init-plan.json"
assert_json "$TMP_ROOT/init-plan.json" \
  '.schema_version == "repo.state_change_plan.v1" and .kind == "initialize" and .apply_allowed == true and .git.clean == true and .git.allowed == true and .commit_created == false and (.digest | startswith("sha256:"))' \
  'initialization dry-run was not clean, digest-bound, and non-committing'
assert_file_absent "$current_root/.repo"
plan_digest="$(jq -r '.digest' "$TMP_ROOT/init-plan.json")"
apply_initialization "$current_root" "$plan_digest" "$TMP_ROOT/init-apply.json"
assert_json "$TMP_ROOT/init-apply.json" \
  '.applied == true and .staged == false and .commit_created == false and (.written | length) > 10' \
  'explicit opt-in did not publish only unstaged, uncommitted files'
assert_file_present "$current_root/.repo/repo.yaml"
assert_equal "$(git -C "$current_root" status --porcelain)" '?? .repo/' \
  'opt-in output was not the only repository change'
[[ -z "$(git -C "$current_root" diff --cached --name-only)" ]] \
  || fail 'opt-in unexpectedly staged portable state'
validate_repository "$current_root" "$TMP_ROOT/opt-in-untracked.json"
assert_json "$TMP_ROOT/opt-in-untracked.json" \
  '.tracking_status == "malformed" and ([.validation.issues[].code] | index("untracked_source") != null)' \
  'unstaged opt-in state was incorrectly accepted as current tracked state'
assert_portable_tree "$current_root"
pass 'opt-in is explicit, digest-reviewed, portable, unstaged, and uncommitted'

log 'committing and validating the current portable pack'
commit_fixture "$current_root" 'fixture: opt in to repository state' .repo
validate_repository "$current_root" "$TMP_ROOT/current.json"
assert_json "$TMP_ROOT/current.json" \
  '.tracking_status == "valid" and .freshness.tracked_state == "clean" and .freshness.head_commit == .freshness.source_commit and (.accepted_context | length) == 0 and .manifest.repository.id == "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV" and .manifest.owner_project.id == "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"' \
  'current pack did not validate against authoritative membership and Git'
assert_portable_tree "$current_root"
pass 'current Git-tracked pack validates with empty accepted semantic context'

log 'rejecting a stale schema envelope without interpreting its body'
stale_root="$TMP_ROOT/stale-pack"
clone_fixture "$current_root" "$stale_root"
sed 's/schema_version: repo.state.v1/schema_version: repo.state.v2/' "$stale_root/.repo/repo.yaml" \
  >"$stale_root/.repo/repo.yaml.next"
mv "$stale_root/.repo/repo.yaml.next" "$stale_root/.repo/repo.yaml"
commit_fixture "$stale_root" 'fixture: stale repository state schema' .repo/repo.yaml
validate_repository "$stale_root" "$TMP_ROOT/stale.json"
assert_json "$TMP_ROOT/stale.json" \
  '.tracking_status == "stale_version" and .manifest == null and ([.validation.issues[].code] | index("unsupported_schema") != null) and .freshness.tracked_state == "unavailable"' \
  'stale pack did not fail closed at the envelope boundary'
pass 'stale pack stops before body and Git interpretation'

log 'rejecting a malformed manifest source before tree and Git observation'
malformed_root="$TMP_ROOT/malformed-source"
clone_fixture "$current_root" "$malformed_root"
printf '%s\n' \
  'kind: loom.repository_state' \
  'kind: duplicate' \
  'schema_version: repo.state.v1' \
  >"$malformed_root/.repo/repo.yaml"
commit_fixture "$malformed_root" 'fixture: malformed repository state manifest' .repo/repo.yaml
validate_repository "$malformed_root" "$TMP_ROOT/malformed.json"
assert_json "$TMP_ROOT/malformed.json" \
  '.tracking_status == "malformed" and ([.validation.issues[].code] | index("manifest_envelope_malformed") != null) and .freshness.tracked_state == "unavailable"' \
  'malformed source did not stop before portable tree and Git interpretation'
pass 'malformed source fails closed without runtime adoption'

log 'making dirty portable state visible without inventing semantic invalidity'
dirty_root="$TMP_ROOT/dirty-repo"
clone_fixture "$current_root" "$dirty_root"
printf '\nUncommitted acceptance observation.\n' >>"$dirty_root/.repo/STATE.md"
validate_repository "$dirty_root" "$TMP_ROOT/dirty.json"
assert_json "$TMP_ROOT/dirty.json" \
  '.tracking_status == "valid" and .freshness.tracked_state == "dirty" and ([.freshness.reasons[]] | index("repo_tree_dirty") != null)' \
  'dirty portable state was not reported as an observed Git fact'
plan_initialization "$dirty_root" "$TMP_ROOT/dirty-allowed-plan.json"
assert_json "$TMP_ROOT/dirty-allowed-plan.json" \
  '.git.clean == false and .git.allowed == true and (.git.changed_paths | index(".repo/STATE.md") != null)' \
  'planned .repo-only dirt was not bounded to reviewed targets'
printf '\nUnrelated dirt.\n' >>"$dirty_root/README.md"
plan_initialization "$dirty_root" "$TMP_ROOT/dirty-blocked-plan.json"
assert_json "$TMP_ROOT/dirty-blocked-plan.json" \
  '.git.clean == false and .git.allowed == false and (.git.blocking_paths | index("README.md") != null) and .apply_allowed == false' \
  'unrelated dirty repository state did not block apply'
pass 'dirty state is observable and unrelated dirt blocks mutation'

log 'classifying an authoritative owner-project mismatch'
owner_root="$TMP_ROOT/owner-mismatch"
clone_fixture "$current_root" "$owner_root"
"$LOOM_BIN" --json repo-state validate \
  --path "$owner_root" \
  --repository-id "$REPOSITORY_ID" \
  --owner-project-id 'project_01ARZ3NDEKTSV4RRFFQ69G5FAA' \
  --owner-project-slug "$PROJECT_SLUG" \
  --role component >"$TMP_ROOT/owner-mismatch.json"
assert_json "$TMP_ROOT/owner-mismatch.json" \
  '.tracking_status == "mismatched_owner" and ([.validation.issues[].code] | index("owner_project_mismatch") != null)' \
  'owner-project mismatch was not classified deterministically'
pass 'owner-project mismatch fails closed without rewriting either source'

log 'proving branch and linked-worktree ownership stays disjoint'
codex_worktree="$TMP_ROOT/codex-worktree"
orca_worktree="$TMP_ROOT/orca-worktree"
git -C "$current_root" branch codex/slice5-fixture
git -C "$current_root" branch orca/slice5-fixture
git -C "$current_root" worktree add -q "$codex_worktree" codex/slice5-fixture
git -C "$current_root" worktree add -q "$orca_worktree" orca/slice5-fixture
printf 'Codex owns this runtime marker.\n' >"$codex_worktree/.codex-runtime-owner"
printf 'ORCA owns this runtime marker.\n' >"$orca_worktree/.orca-runtime-owner"
assert_equal "$(git -C "$codex_worktree" rev-parse --show-toplevel)" "$codex_worktree" \
  'Codex fixture worktree disagrees with Git about its root'
assert_equal "$(git -C "$orca_worktree" rev-parse --show-toplevel)" "$orca_worktree" \
  'ORCA fixture worktree disagrees with Git about its root'
assert_equal "$(git -C "$codex_worktree" branch --show-current)" 'codex/slice5-fixture' \
  'Codex fixture worktree disagrees with Git about its branch'
assert_equal "$(git -C "$orca_worktree" branch --show-current)" 'orca/slice5-fixture' \
  'ORCA fixture worktree disagrees with Git about its branch'
assert_file_present "$codex_worktree/.codex-runtime-owner"
assert_file_absent "$codex_worktree/.orca-runtime-owner"
assert_file_present "$orca_worktree/.orca-runtime-owner"
assert_file_absent "$orca_worktree/.codex-runtime-owner"
worktree_count="$(git -C "$current_root" worktree list --porcelain | grep -c '^worktree ')"
assert_equal "$worktree_count" '3' 'Git did not retain the original plus two disjoint linked worktrees'
validate_repository "$codex_worktree" "$TMP_ROOT/codex-worktree.json"
validate_repository "$orca_worktree" "$TMP_ROOT/orca-worktree.json"
assert_json "$TMP_ROOT/codex-worktree.json" \
  '.tracking_status == "valid" and .freshness.tracked_state == "clean"' \
  'Codex fixture worktree portable state did not agree with Git'
assert_json "$TMP_ROOT/orca-worktree.json" \
  '.tracking_status == "valid" and .freshness.tracked_state == "clean"' \
  'ORCA fixture worktree portable state did not agree with Git'
assert_portable_tree "$codex_worktree" "$codex_worktree" "$orca_worktree"
assert_portable_tree "$orca_worktree" "$codex_worktree" "$orca_worktree"
pass 'multiple branches and worktrees retain one Git-visible owner each'

log 'proving archived harness runtime remains outside portable repository state'
archive_root="$TMP_ROOT/archived-thread"
clone_fixture "$current_root" "$archive_root"
validate_repository "$archive_root" "$TMP_ROOT/archive-before.json"
mv "$TMP_ROOT/runtime/live/orca-session" "$TMP_ROOT/runtime/archive/orca-session"
mv "$TMP_ROOT/runtime/live/codex-session" "$TMP_ROOT/runtime/archive/codex-session"
validate_repository "$archive_root" "$TMP_ROOT/archive-after.json"
jq -S '{tracking_status, manifest, accepted_context, head_commit: .freshness.head_commit, source_commit: .freshness.source_commit, tracked_state: .freshness.tracked_state}' \
  "$TMP_ROOT/archive-before.json" >"$TMP_ROOT/archive-before.stable.json"
jq -S '{tracking_status, manifest, accepted_context, head_commit: .freshness.head_commit, source_commit: .freshness.source_commit, tracked_state: .freshness.tracked_state}' \
  "$TMP_ROOT/archive-after.json" >"$TMP_ROOT/archive-after.stable.json"
cmp -s "$TMP_ROOT/archive-before.stable.json" "$TMP_ROOT/archive-after.stable.json" \
  || fail 'archiving runtime thread state changed the portable repository projection'
assert_portable_tree "$archive_root"
pass 'archived thread/session state remains runtime-only and non-portable'

printf '[repo-development-state] PASS (%d checks)\n' "$PASS_COUNT"
