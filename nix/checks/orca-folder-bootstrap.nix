{ pkgs, orca ? pkgs.callPackage ../packages/orca-headless.nix { } }:

# This derivation checks the exact packaged source, including reverse-patch
# failure and generated-wrapper routing (executable boundaries substituted).
# Electron/FHS execution and native persistence require the separate
# integrator-coordinated Linux runtime gate; a Nix sandbox is not that runner.
pkgs.runCommand "orca-1.4.191-folder-bootstrap-contract" {
  nativeBuildInputs = [ pkgs.nodejs pkgs.patch pkgs.bash ];
} ''
  mkdir -p source/tests/smoke source/nix/patches
  cp ${../../tests/smoke/v2_orca_folder_bootstrap_local.sh} source/tests/smoke/v2_orca_folder_bootstrap_local.sh
  cp ${../../tests/smoke/orca_folder_bootstrap_contract.cjs} source/tests/smoke/orca_folder_bootstrap_contract.cjs
  cp ${../patches/orca-folder-bootstrap.patch} source/nix/patches/orca-folder-bootstrap.patch
  bash source/tests/smoke/v2_orca_folder_bootstrap_local.sh \
    ${orca.unpatchedAppDir} ${orca.appDir} ${orca.appRun} ${orca.appDir} > receipt.jsonl
  mkdir -p "$out"
  cp receipt.jsonl "$out/receipt.jsonl"
''
