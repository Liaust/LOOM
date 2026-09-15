#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WATCHDOG="$REPO_DIR/scripts/loom-network-watchdog"
INSTALL_DIR="/Library/PrivilegedHelperTools"
INSTALLED_WATCHDOG="$INSTALL_DIR/local.loom.network-watchdog"
PLIST="/Library/LaunchDaemons/local.loom.network-watchdog.plist"
LABEL="local.loom.network-watchdog"
USER_HOME="${LOOM_USER_HOME:-$HOME}"
WG_CONF="${LOOM_MACBOOK_WG_CONF:-$USER_HOME/.loom/secrets/wireguard/loomwg0.conf}"
TMP_PLIST="$(mktemp)"

mkdir -p "$USER_HOME/.loom/logs" "$USER_HOME/.loom/run"

cat >"$TMP_PLIST" <<PLIST_XML
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>$INSTALLED_WATCHDOG</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>LOOM_USER_HOME</key>
    <string>$USER_HOME</string>
    <key>LOOM_MACBOOK_WG_CONF</key>
    <string>$WG_CONF</string>
    <key>LOOM_MACBOOK_WG_IP</key>
    <string>${LOOM_MACBOOK_WG_IP:-10.44.0.5}</string>
    <key>LOOM_MAIN_SMB_HOST</key>
    <string>${LOOM_MAIN_SMB_HOST:-loom-storage}</string>
    <key>LOOM_MACBOOK_HELPER</key>
    <string>$REPO_DIR/scripts/loom-macbook</string>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$USER_HOME/.loom/logs/network-watchdog.launchd.log</string>
  <key>StandardErrorPath</key>
  <string>$USER_HOME/.loom/logs/network-watchdog.launchd.err</string>
</dict>
</plist>
PLIST_XML

chmod 644 "$TMP_PLIST"

needs_install=1
if [[ -f "$INSTALLED_WATCHDOG" && -f "$PLIST" ]] && cmp -s "$WATCHDOG" "$INSTALLED_WATCHDOG"; then
  needs_install=0
fi

if [[ "$needs_install" -eq 1 ]]; then
  osascript <<APPLESCRIPT
do shell script "launchctl bootout system '$PLIST' >/dev/null 2>&1 || true; sleep 1; mkdir -p '$INSTALL_DIR'; cp '$WATCHDOG' '$INSTALLED_WATCHDOG'; chown root:wheel '$INSTALLED_WATCHDOG'; chmod 755 '$INSTALLED_WATCHDOG'; xattr -c '$INSTALLED_WATCHDOG' >/dev/null 2>&1 || true; cp '$TMP_PLIST' '$PLIST'; chown root:wheel '$PLIST'; chmod 644 '$PLIST'; xattr -c '$PLIST' >/dev/null 2>&1 || true; sync" with administrator privileges
APPLESCRIPT
  sleep 5
fi

osascript <<APPLESCRIPT
do shell script "launchctl enable system/$LABEL >/dev/null 2>&1 || true; if launchctl print system/$LABEL >/dev/null 2>&1; then launchctl kickstart -k system/$LABEL >/dev/null 2>&1 || true; else launchctl bootstrap system '$PLIST' || (launchctl bootout system '$PLIST' >/dev/null 2>&1 || true; launchctl bootout system/$LABEL >/dev/null 2>&1 || true; sleep 2; launchctl bootstrap system '$PLIST'); launchctl kickstart -k system/$LABEL >/dev/null 2>&1 || true; fi" with administrator privileges
APPLESCRIPT

rm -f "$TMP_PLIST"

echo "LOOM network watchdog is running."
