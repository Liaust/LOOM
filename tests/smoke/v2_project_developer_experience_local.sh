#!/usr/bin/env bash
# Real, disposable G2a prerequisites; models require a committed fixture checkpoint.
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
exec python3 "$repo_root/tests/acceptance/project_dx/harness.py" "$@"
