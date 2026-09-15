#!/usr/bin/env bash

set -euo pipefail

umask 077

pass_cli=${PROTON_PASS_CLI_BIN:-pass-cli}
stat_bin=${PROTON_PASS_STAT_BIN:-stat}
token_file=${PROTON_PASS_AGENT_TOKEN_FILE:-"${HOME:?}/.config/proton-pass-cli/agent.pat"}
state_dir=${PROTON_PASS_AGENT_STATE_DIR:-"${HOME}/.local/state/proton-pass-cli"}
lock_file=${PROTON_PASS_AGENT_LOCK_FILE:-"${state_dir}/session.lock"}

die() {
  printf 'pass-session-ensure: %s\n' "$*" >&2
  exit 1
}

[[ $# -eq 0 ]] || die "this command does not accept arguments"
[[ -x $(command -v "$pass_cli" 2>/dev/null) ]] || die "pass-cli is unavailable"
[[ ! -L $state_dir ]] || die "state directory must not be a symlink"

install -d -m 0700 "$state_dir"
[[ ! -L $lock_file ]] || die "session lock must not be a symlink"

exec 9>"$lock_file"
flock -x 9

if "$pass_cli" info >/dev/null 2>&1; then
  printf 'Proton Pass session ready.\n'
  exit 0
fi

[[ -f $token_file && ! -L $token_file ]] ||
  die "agent access token file is missing or unsafe"
[[ $("$stat_bin" -c '%a' "$token_file") == 600 ]] ||
  die "agent access token file must have mode 0600"
[[ $("$stat_bin" -c '%u' "$token_file") == "$(id -u)" ]] ||
  die "agent access token file must be owned by the current user"
[[ $("$stat_bin" -c '%g' "$token_file") == "$(id -g)" ]] ||
  die "agent access token file must use the current user's primary group"

token_size=$("$stat_bin" -c '%s' "$token_file")
(( token_size > 0 && token_size <= 4096 )) ||
  die "agent access token file has an invalid size"

mapfile -t token_lines <"$token_file"
[[ ${#token_lines[@]} -eq 1 && -n ${token_lines[0]} ]] ||
  die "agent access token file must contain exactly one non-empty line"

token=${token_lines[0]}
unset token_lines
trap 'unset token' EXIT

"$pass_cli" logout --force >/dev/null 2>&1 || true
PROTON_PASS_PERSONAL_ACCESS_TOKEN=$token "$pass_cli" login >/dev/null
unset token
trap - EXIT

"$pass_cli" info >/dev/null 2>&1 ||
  die "login completed without a usable Proton Pass session"

printf 'Proton Pass session restored.\n'
