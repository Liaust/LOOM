#!/usr/bin/env bash
set -euo pipefail

result_file="${LOOM_RESULT_FILE:-}"
if [ -z "$result_file" ]; then
  printf '{"status":"ok","outputs":{"message":"hello from LOOM project workflow"},"artifacts":[]}\n'
  exit 0
fi

cat > "$result_file" <<'JSON'
{
  "status": "ok",
  "outputs": {
    "message": "hello from LOOM project workflow"
  },
  "artifacts": []
}
JSON
