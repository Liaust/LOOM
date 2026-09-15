#!/usr/bin/env bash
set -euo pipefail

# Public compiled-source contract only; never discovers or contacts an installed
# Orca. Functional Linux acceptance is a separate integrator-owned gate.
if [[ $# != 2 && $# != 4 ]]; then
  echo "usage: $0 ORIGINAL_APPDIR PATCHED_APPDIR [GENERATED_WRAPPER IMMUTABLE_APPDIR]" >&2
  exit 2
fi
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
original=$(cd -- "$1" && pwd)
patched=$(cd -- "$2" && pwd)
contract="$script_dir/orca_folder_bootstrap_contract.cjs"
patch_file="$repo_root/nix/patches/orca-folder-bootstrap.patch"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/loom-orca-folder-reverse.XXXXXX")
cleanup() {
  case "$fixture" in */loom-orca-folder-reverse.*) ;; *) exit 1 ;; esac
  rm -rf -- "$fixture"
  [[ ! -e "$fixture" ]]
}
trap cleanup EXIT

node "$contract" baseline "$original"
node "$contract" patched "$patched" "$original"

# Copy only public CLI/shared source. Link the archive for read-only dispatcher
# inspection; never reverse-patch the packaged or original immutable tree.
mkdir -p "$fixture/resources/app.asar.unpacked"
cp -R "$patched/resources/app.asar.unpacked/out" "$fixture/resources/app.asar.unpacked/"
chmod -R u+w "$fixture/resources/app.asar.unpacked/out"
ln -s "$patched/resources/app.asar" "$fixture/resources/app.asar"
patch --batch --fuzz=0 -R -p1 -d "$fixture" < "$patch_file" > "$fixture/reverse.log"
node "$contract" baseline "$fixture"
if node "$contract" patched "$fixture" "$original" > "$fixture/positive.log" 2>&1; then
  echo "reversed patch unexpectedly passed the public folder positive" >&2
  exit 1
fi
if ! grep -Eq '(Unknown|Unsupported|unknown|unsupported) flag.*kind' "$fixture/positive.log"; then
  cat "$fixture/positive.log" >&2
  exit 1
fi
patch --batch --forward --fuzz=0 --dry-run -p1 -d "$fixture" < "$patch_file" > "$fixture/forward-dry-run.log"
if grep -Eiq 'offset|fuzz|FAILED' "$fixture/forward-dry-run.log"; then
  cat "$fixture/forward-dry-run.log" >&2
  exit 1
fi
patch --batch --forward --fuzz=0 -p1 -d "$fixture" < "$patch_file" > "$fixture/forward.log"
node "$contract" patched "$fixture"
if [[ $# == 4 ]]; then
  node "$contract" dispatch "$patched" "$3" "$4"
else
  echo 'INCOMPLETE: generated-wrapper gate requires GENERATED_WRAPPER IMMUTABLE_APPDIR'
fi
echo 'PASS: source matrix, reverse sentinel and zero-fuzz reapply; Linux runtime acceptance NOT exercised'
