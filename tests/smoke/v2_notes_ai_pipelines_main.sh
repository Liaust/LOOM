#!/usr/bin/env bash
set -euo pipefail

if [[ "${LOOM_MAIN_NOTES_AI_ACCEPTANCE:-}" != "1" ]]; then
  printf 'Refusing hardware acceptance. Set LOOM_MAIN_NOTES_AI_ACCEPTANCE=1 after reviewing the feature handoff.\n' >&2
  exit 2
fi

command -v loom >/dev/null
command -v pdfinfo >/dev/null
command -v pdftotext >/dev/null
command -v pdftoppm >/dev/null
command -v tesseract >/dev/null

loom notes pipelines status
loom notes pipelines policy status
loom notes pipelines backfill

printf 'Dry-run complete. Review counts, model sizes, backups, database health, and the acceptance checklist.\n'
printf 'Apply is intentionally operator-controlled: loom notes pipelines backfill --apply --yes\n'
