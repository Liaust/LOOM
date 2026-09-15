#!/usr/bin/env bash
set -euo pipefail

# Existing pinned tooling only. No build, model, gateway, accounts or live data.
cd "$(dirname "$0")/.."
if [[ -z "${LOOM_TEST_HERMES_BINARY:-}" ]]; then
  nix_bin="$(command -v nix || true)"
  if [[ -z "$nix_bin" && -x /nix/var/nix/profiles/default/bin/nix ]]; then
    nix_bin=/nix/var/nix/profiles/default/bin/nix
  fi
  [[ -n "$nix_bin" ]] || { echo 'Required existing pinned Hermes package unavailable (no Nix resolver).' >&2; exit 1; }
  native_package="$($nix_bin --extra-experimental-features 'nix-command flakes' eval --offline --no-write-lock-file --raw .#packages."$(uname -m | sed 's/arm64/aarch64/')"-"$(uname -s | tr '[:upper:]' '[:lower:]')".hermes-agent.outPath)"
  export LOOM_TEST_HERMES_BINARY="$native_package/bin/hermes"
fi
case "$LOOM_TEST_HERMES_BINARY" in
  /nix/store/*-hermes-agent-0.21.0/bin/hermes) ;;
  *) echo 'Required pinned Nix Hermes 0.21.0 binary path.' >&2; exit 1 ;;
esac
snapshot_helper="$(dirname "$LOOM_TEST_HERMES_BINARY")/loom-hermes-snapshot-db"
[[ -x "$LOOM_TEST_HERMES_BINARY" && -x "$snapshot_helper" ]] || { echo 'Required pinned tooling is not already built; refusing bootstrap.' >&2; exit 1; }
pinned_python="$(head -n 1 "$snapshot_helper")"
pinned_python="${pinned_python#\#!}"
if [[ -n "${LOOM_TEST_HERMES_PYTHON:-}" && "$LOOM_TEST_HERMES_PYTHON" != "$pinned_python" ]]; then
  echo 'Hermes Python conflicts with the pinned snapshot helper.' >&2; exit 1
fi
export LOOM_TEST_HERMES_PYTHON="$pinned_python"
export PYTHONDONTWRITEBYTECODE=1 GOMAXPROCS=2
[[ -x "$LOOM_TEST_HERMES_PYTHON" ]] || { echo 'Required pinned Python is absent.' >&2; exit 1; }
# No Hermes import outside a disposable profile: this is a byte comparison only.
"$LOOM_TEST_HERMES_PYTHON" - "$snapshot_helper" <<'PY'
import pathlib,sys
helper=pathlib.Path(sys.argv[1]).read_bytes().split(b'\n',1)[1]
assert helper==pathlib.Path('internal/hermesprofile/native_snapshot.py').read_bytes(), 'Snapshot helper differs from accepted source'
PY
go test -p 2 -count=1 -v -run '^TestMinaNativeRecoveryCLIBackupVerifyImport$' ./internal/hermesprofile/cmd/loom-hermes-recovery
