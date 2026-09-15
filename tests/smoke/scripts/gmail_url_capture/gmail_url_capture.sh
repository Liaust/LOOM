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

json_string() {
  key="$1"
  printf '%s' "$LOOM_INPUT_JSON" |
    sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" |
    head -n 1
}

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

message_id="$(json_string message_id)"
email_id="$(json_string email_id)"
url="$(json_string url)"
token="$(json_string token)"

if [ -z "$message_id" ] || [ -z "$email_id" ] || [ -z "$url" ] || [ -z "$token" ]; then
  echo "gmail_url_capture missing required payload fields" >&2
  exit 1
fi

case "$url" in
  http://127.0.0.1/*|https://127.0.0.1/*|http://localhost/*|https://localhost/*) ;;
  *)
    echo "gmail_url_capture only accepts local smoke URLs" >&2
    exit 1
    ;;
esac

echo "gmail_url_capture message_id=$message_id"
echo "gmail_url_capture email_id=$email_id"
echo "gmail_url_capture url=$url"
echo "gmail_url_capture token=$token"

safe_message_id="$(json_escape "$message_id")"
safe_email_id="$(json_escape "$email_id")"
safe_url="$(json_escape "$url")"
safe_token="$(json_escape "$token")"

printf '{"status":"ok","outputs":{"message_id":"%s","email_id":"%s","url":"%s","token":"%s"}}\n' \
  "$safe_message_id" \
  "$safe_email_id" \
  "$safe_url" \
  "$safe_token" > "$LOOM_RESULT_FILE"
