#!/usr/bin/env bash
# Source/local fixtures only. Never run this on Main or against a live profile.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
cd "$repo"
py=/nix/store/5c4bns88zxadxva4cghfsgc4bi719gy8-hermes-agent-env/bin/python3
hermes=/nix/store/b9z4vy2wns4zd92ya4lh2bc9lfrkb2jr-hermes-agent-0.21.0
connector=/nix/store/nbmnpy28wbrv87zhgsng61kqizx75ssw-loom-mac-computer-use-bridge-0.1.0
if [[ ${1:-} == --source-only ]]; then
  [[ $# == 1 && -x "$py" ]] || exit 1
  # Explicit partial gate: never label this packaged/runtime acceptance.
  env -u LOOM_NATIVE_STRESS_ONLY -u LOOM_STARTUP_ERROR_STRESS_ONLY \
    LOOM_NATIVE_TEST_MODE=remote PYTHONPATH="$repo/tests/hermes_mac_computer_use" \
    HERMES_DISABLE_LAZY_INSTALLS=1 HERMES_SKIP_NODE_BOOTSTRAP=1 \
    "$py" -B -m unittest test_native_contract test_mac_endpoint test_setup_contract \
      test_packaging_contract test_transport_contract test_integration_acceptance
  printf 'PASS: source/native contracts only. Packaged launcher/real desktop acceptance NOT RUN.\n'
  exit 0
fi
[[ $# == 0 ]] || { printf 'Usage: %s [--source-only]\n' "$0" >&2; exit 2; }
for required in "$py" "$hermes/bin/hermes" "$connector/bin/loom-mac-computer-use-bridge"; do
  if [[ ! -x "$required" ]]; then
    printf 'Missing required cached dependency: %s; stop, do not bootstrap.\n' "$required" >&2
    exit 1
  fi
done
mkdir -p "$repo/.loom-acceptance/main-mac-w5"
receipt=$(mktemp -d "$repo/.loom-acceptance/main-mac-w5/combined.XXXXXX")
mkdir "$receipt/home"
# Keep the OS-owned temporary anchor: shared /tmp correctly fails key custody.
fixture_tmp=$(mktemp -d "${TMPDIR:?OS-owned TMPDIR required}/loom-w5.XXXXXX")
printf 'Retained private fixture temporary root: %s\n' "$fixture_tmp"
printf 'Retained disposable combined acceptance: %s\n' "$receipt"

# Refresh actual Nix render for the existing packaged-native runtime fixtures.
GOMAXPROCS=2 go test -p 2 -count=1 -timeout=20m ./internal/config \
  -run '^TestNixMacComputerUseRuntimeRender$' > "$receipt/render.log" 2>&1
cp "$repo/.loom-acceptance/main-mac-wiring/render.json" "$receipt/render.json"
# No inherited native/stress selectors or profile credentials enter Python.
fixture_python() {
  env -i PATH="$PATH" HOME="$receipt/home" HERMES_HOME="$receipt/home" \
    TMPDIR="$fixture_tmp" GOMAXPROCS=2 HERMES_DISABLE_LAZY_INSTALLS=1 HERMES_SKIP_NODE_BOOTSTRAP=1 \
    "$py" -B "$@"
}
fixture_python -m unittest discover -s tests/hermes_mac_computer_use -p 'test_*.py' -v \
  > "$receipt/python.log" 2>&1
# Fail on an unexecuted required case or any new skip/XFAIL, including selectors
# accidentally restricting discovery. The existing nested gate checks its own
# required patched suite independently; frozen upstream failures stay separate.
fixture_python - "$receipt/python.log" <<'PY'
import pathlib,re,sys
log=pathlib.Path(sys.argv[1]).read_text()
assert re.search(r'Ran 113 tests in ',log), 'outer collection changed'
assert 'Patched native subprocess: 121 tests in ' in log, 'required native suite not executed'
assert 'Patched source, remote absent: 40 tests in ' in log
assert len(re.findall(r'(?:^|\.\.\. )expected failure$',log,re.M)) == 16
assert 'OK (expected failures=16)' in log
assert not re.search(r'\.\.\. skipped|skipped=|unexpected success',log)
assert re.search(r'^test_ending_one_session_keeps_other_session_usable .* \.\.\. ok$',log,re.M)
print('PASS: 113 outer tests (97 passes, 16 frozen upstream-only expected failures); 121 required patched-native passes, zero skips/XFAIL; remote-absent baseline 24 passes + same 16 expected failures.')
PY
fixture_python tests/hermes_mac_computer_use/test_packaging_contract.py \
  --installed "$hermes" "$connector" > "$receipt/installed.log" 2>&1
bash tests/smoke/mina_workspace_projection_local.sh > "$receipt/projection.log" 2>&1
printf 'PASS: actual package/import, selected/default-off/other-profile, native image/errors/delivery, session survival, bounded teardown, and MINA source reconstruction.\n'
printf 'Unobserved: real Linux namespace/SSH, Mac app/TCC/GUI, live gateway/TUI and model routing (H01-H04).\n'
