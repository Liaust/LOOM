#!/bin/bash
set -euo pipefail

# No GC, profile removal, installer replay or credential changes in this repair.
if [[ "$(/usr/bin/uname -s)" != Darwin || "$EUID" -ne 0 ]]; then
  printf '%s\n' 'Run this reviewed repair as a Mac administrator.' >&2
  exit 1
fi
readonly profile=/nix/var/nix/profiles/default
readonly fallback=https://releases.nixos.org/nix/nix-2.34.9/fallback-paths.nix
readonly receipt_dir=/var/db/loom-security
/bin/mkdir -p "$receipt_dir"
/bin/chmod 700 "$receipt_dir"
umask 077
exec > >(/usr/bin/tee -a "$receipt_dir/nix-repair-20261108.log") 2>&1

printf '%s\n' 'Repairing vendor-described multi-user store ownership (no symlink following).'
/usr/bin/find -x /nix/store -mindepth 1 ! -user root -exec /usr/sbin/chown -h 0:0 {} +
if [[ -n "$(/usr/bin/find -x /nix/store -mindepth 1 ! -user root -print -quit)" ]]; then
  printf '%s\n' 'Store still contains non-root members; stopping before upgrade.' >&2
  exit 1
fi
"$profile/bin/nix" --version
"$profile/bin/nix" --extra-experimental-features nix-command upgrade-nix \
  --profile "$profile" --nix-store-paths-url "$fallback"
"$profile/bin/nix" --version
case "$("$profile/bin/nix" --version)" in
  'nix (Nix) 2.34.9') ;;
  *) printf '%s\n' 'Unexpected selected version; preserve the profile and inspect before restarting.' >&2; exit 1 ;;
esac
/bin/launchctl kickstart -k system/org.nixos.nix-daemon
/bin/launchctl print system/org.nixos.nix-daemon | /usr/bin/sed -n '/state =/p; /pid =/p; /last exit code/p'
printf '%s\n' 'Repair complete; verify the running daemon path/version and normal Nix use separately.'
