{ lib, runCommand, bash, coreutils, cacert, git, gh, basecamp-cli, agentIdentity ? "morathustra", basecampAccountID }:

assert builtins.elem agentIdentity [ "morathustra" "mina" ];
let
  identity = agentIdentity;
  makeTool = tool: native: runCommand "loom-${identity}-${tool}" { } ''
    mkdir -p "$out/bin"
    substitute ${../files/morathustra-account-tool.sh} "$out/bin/loom-${identity}-${tool}" \
      --replace-fail '@bash@' '${bash}' \
      --replace-fail '@coreutils@' '${coreutils}' \
      --replace-fail '@cacert@' '${cacert}' \
      --replace-fail '@authRoot@' '/var/lib/loom-${identity}-auth' \
      --replace-fail '@identity@' '${identity}' \
      --replace-fail '@basecampAccountID@' '${toString basecampAccountID}' \
      --replace-fail '@retired@' 'false' \
      --replace-fail '@owner@' 'agents' \
      --replace-fail '@tool@' '${tool}' \
      --replace-fail '@native@' '${native}' \
      --replace-fail '@childPath@' '${lib.makeBinPath [ coreutils git ]}'
    chmod 0555 "$out/bin/loom-${identity}-${tool}"
    ${bash}/bin/bash -n "$out/bin/loom-${identity}-${tool}"
    ${lib.optionalString (identity == "mina") ''
      substitute ${../files/morathustra-account-tool.sh} "$out/bin/loom-morathustra-${tool}" \
        --replace-fail '@bash@' '${bash}' \
        --replace-fail '@retired@' 'true'
      chmod 0555 "$out/bin/loom-morathustra-${tool}"
    ''}
  '';
in [
  (makeTool "gh" "${gh}/bin/gh")
  (makeTool "basecamp" "${basecamp-cli}/bin/basecamp")
]
