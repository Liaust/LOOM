#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
helper="$repo_root/nix/files/agents-pass-session-ensure.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin" "$tmp/runtime"
cat >"$tmp/bin/pass-cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case ${1:-} in
  info)
    [[ -f $FAKE_PASS_STATE/authenticated ]]
    ;;
  logout)
    rm -f "$FAKE_PASS_STATE/authenticated"
    ;;
  login)
    [[ ${PROTON_PASS_PERSONAL_ACCESS_TOKEN:-} == test-agent-token ]]
    sleep 0.1
    printf 'login\n' >>"$FAKE_PASS_STATE/logins"
    : >"$FAKE_PASS_STATE/authenticated"
    ;;
  *)
    exit 2
    ;;
esac
EOF
chmod 0755 "$tmp/bin/pass-cli"

token_file="$tmp/agent.pat"
printf '%s\n' test-agent-token >"$token_file"
chmod 0600 "$token_file"

run_helper() {
  HOME="$tmp/home" \
  FAKE_PASS_STATE="$tmp/runtime" \
  PROTON_PASS_CLI_BIN="$tmp/bin/pass-cli" \
  PROTON_PASS_STAT_BIN="${PROTON_PASS_STAT_BIN:-stat}" \
  PROTON_PASS_AGENT_TOKEN_FILE="$token_file" \
  PROTON_PASS_AGENT_STATE_DIR="$tmp/state" \
  "$helper"
}

run_helper | grep -q 'session restored'
[[ $(wc -l <"$tmp/runtime/logins") -eq 1 ]]

run_helper | grep -q 'session ready'
[[ $(wc -l <"$tmp/runtime/logins") -eq 1 ]]

rm -f "$tmp/runtime/authenticated"
run_helper >"$tmp/first.out" &
first_pid=$!
run_helper >"$tmp/second.out" &
second_pid=$!
wait "$first_pid"
wait "$second_pid"
[[ $(wc -l <"$tmp/runtime/logins") -eq 2 ]]

rm -f "$tmp/runtime/authenticated"
chmod 0644 "$token_file"
if run_helper >"$tmp/unsafe.out" 2>"$tmp/unsafe.err"; then
  printf 'unsafe token permissions were accepted\n' >&2
  exit 1
fi
grep -q 'mode 0600' "$tmp/unsafe.err"
[[ $(wc -l <"$tmp/runtime/logins") -eq 2 ]]

chmod 0600 "$token_file"
printf 'first\nsecond\n' >"$token_file"
if run_helper >"$tmp/multiline.out" 2>"$tmp/multiline.err"; then
  printf 'multi-line token file was accepted\n' >&2
  exit 1
fi
grep -q 'exactly one non-empty line' "$tmp/multiline.err"

if rg -q 'test-agent-token' "$tmp"/*.out "$tmp"/*.err 2>/dev/null; then
  printf 'token value leaked into helper output\n' >&2
  exit 1
fi

printf 'proton pass session ensure smoke: PASS\n'
