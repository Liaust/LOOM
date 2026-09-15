#!/usr/bin/env sh
set -eu

if [ -z "${LOOM_INPUT_JSON:-}" ]; then
  echo "LOOM_INPUT_JSON is required" >&2
  exit 1
fi

if [ -z "${LOOM_RESULT_FILE:-}" ]; then
  echo "LOOM_RESULT_FILE is required" >&2
  exit 1
fi

message_id="$(printf '%s' "$LOOM_INPUT_JSON" | sed -n 's/.*"message_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
url="$(printf '%s' "$LOOM_INPUT_JSON" | sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
token="$(printf '%s' "$LOOM_INPUT_JSON" | grep -o 'slicesevendirectevent[a-zA-Z0-9_]*' | head -n 1 || true)"

echo "direct_event_echo message_id=${message_id:-missing}"
echo "direct_event_echo url=${url:-missing}"
if [ -n "$token" ]; then
  echo "direct_event_echo token=$token"
fi

safe_message_id="$(printf '%s' "$message_id" | sed 's/\\/\\\\/g; s/"/\\"/g')"
safe_url="$(printf '%s' "$url" | sed 's/\\/\\\\/g; s/"/\\"/g')"
safe_token="$(printf '%s' "$token" | sed 's/\\/\\\\/g; s/"/\\"/g')"

printf '{"status":"ok","outputs":{"message_id":"%s","url":"%s","token":"%s"}}\n' "$safe_message_id" "$safe_url" "$safe_token" > "$LOOM_RESULT_FILE"
