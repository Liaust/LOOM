#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "$tmp_dir"' EXIT

fail() {
  printf '[fail] main Box intake retirement: %s\n' "$*" >&2
  exit 1
}

cd "$repo_root"

go test -count=1 ./internal/loomcli ./internal/loomcli/portal ./internal/setup ./internal/loomdocs
go build -o "$tmp_dir/loom" ./cmd/loom

"$tmp_dir/loom" box --help >"$tmp_dir/box-help.txt"
if grep -Eiq 'dropzone' "$tmp_dir/box-help.txt"; then
  fail 'Box help advertises retired Dropzone commands'
fi

main_box="$tmp_dir/main-box"
workspace_box="$tmp_dir/workspace-box"
"$tmp_dir/loom" --json box init --path "$main_box" --profile main >"$tmp_dir/main-init.json"
"$tmp_dir/loom" --json box init --path "$workspace_box" --profile workspace >"$tmp_dir/workspace-init.json"

[[ ! -e "$main_box/Dropzone" && ! -e "$main_box/loom-lane" ]] ||
  fail 'main Box init recreated a retired intake path'
[[ -d "$workspace_box/loom-lane" && ! -e "$workspace_box/Dropzone" ]] ||
  fail 'workspace Box did not preserve Lane-only intake topology'

jq -e '
  .status_after.profile == "main" and
  .status_after.lane == null and
  .status_after.dropzone_transfers == null and
  ([.status_after.areas[].key] | index("lane") | not) and
  ([.status_after.areas[].key] | index("dropzone") | not)
' "$tmp_dir/main-init.json" >/dev/null

if grep -Eq '\$\{cfg\.boxPath\}/(Dropzone|loom-lane|LOOM Lane)' nix/modules/loom-storage.nix; then
  fail 'Nix tmpfiles source declares a retired Main Box intake path'
fi
grep -Fq 'LOOM Main Box tmpfiles must not recreate retired Dropzone or Main Lane paths.' nix/modules/loom-storage.nix ||
  fail 'Nix source is missing the retired-intake topology assertion'

if command -v nix >/dev/null 2>&1; then
  nix --extra-experimental-features 'nix-command flakes' eval --json --impure --expr '
    let
      flake = (import ./tests/nix/source-flake.nix {});
      rules = flake.nixosConfigurations.hardware-main.config.systemd.tmpfiles.rules;
    in rules
  ' >"$tmp_dir/tmpfiles.json"
  jq -e '
    any(.[]; contains("/srv/loom/storage/imports")) and
    (all(.[]; (contains("/srv/loom/box/Dropzone") or contains("/srv/loom/box/loom-lane")) | not))
  ' "$tmp_dir/tmpfiles.json" >/dev/null ||
    fail 'hardware-main tmpfiles do not preserve Imports-only Main intake topology'
else
  printf '[skip] nix is unavailable; skipped hardware-main tmpfiles evaluation\n'
fi

printf '[ok] Main Box uses Storage Imports, workspace Box preserves Lane, and cleanup surfaces stay plan-bound\n'
