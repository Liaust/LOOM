#!/usr/bin/env bash
set -euo pipefail

# Only tiny local profiles. No daemon, Main, Borg, or cloud transfer.
cd "$(dirname "$0")/../.."
: "${LOOM_TEST_HERMES_BINARY:?Set this to the locally built pinned Nix Hermes binary}"
: "${LOOM_TEST_HERMES_PYTHON:?Set this to the pinned Hermes Python environment}"
export PYTHONDONTWRITEBYTECODE=1
"$LOOM_TEST_HERMES_PYTHON" internal/hermesprofile/native_snapshot_test.py
go test -count=1 -v -run '^TestCaptureNativeHermesLive$' ./internal/hermesprofile
