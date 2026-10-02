#!/bin/sh
set -eu
project_root=$(CDPATH= cd ../.. && pwd -P)
exec /run/current-system/sw/bin/loom-codex-exec \
  --project-root "$project_root" --prompt-file prompt.txt \
  --sandbox read-only --timeout-seconds 90 --loom-job-result --require-text LOOM_CODEX_SCHEDULE_OK
