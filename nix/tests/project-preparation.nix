# Rendered contract, realized packaging check and exact units for the
# integrator-owned disposable Linux fixture. Evaluation alone does not run the
# executable/link proof. This never activates a host service or VM.
{ pkgs, lib, self, fixtureUIDBase ? 268435456 }:
let
  system = pkgs.stdenv.hostPlatform.system;
  evaluate = enabled: lib.nixosSystem {
    inherit system;
    specialArgs = { inherit self; };
    modules = [ ../modules/loom-project-preparation.nix {
      system.stateVersion = "25.11";
      boot.isContainer = true;
      loom.projectPreparation = { enable = enabled; callerUID = 1234; uidBase = fixtureUIDBase; };
    } ];
  };
  enabled = (evaluate true).config;
  disabled = (evaluate false).config;
  socket = enabled.systemd.units."loom-project-preparation.socket".text;
  helper = enabled.systemd.units."loom-project-preparation.service".text;
  hostConfig = enabled.environment.etc."loom/project-preparation.json".text;
  policy = builtins.fromJSON (builtins.unsafeDiscardStringContext hostConfig);
  runtime = import ../lib/project-preparation-runtime.nix { inherit pkgs; };
  containsAll = text: parts: builtins.all (p: lib.hasInfix p text) parts;
  # Also exported for an integrator to check an already-realized original root.
  # It runs only the fixed getent version probe, with no NSS lookup, chroot,
  # PATH fallback or service activation.
  getentLinkCheck = pkgs.writeShellScript "loom-preparation-getent-link-check" ''
    set -euo pipefail
    runtimeRoot="$1"
    expectedGetent="$2"
    test -f "$expectedGetent"
    test -x "$expectedGetent"
    test -L "$runtimeRoot/usr/bin/getent"
    test "$(${pkgs.coreutils}/bin/readlink "$runtimeRoot/usr/bin/getent")" = "$expectedGetent"
    test -f "$runtimeRoot/usr/bin/getent"
    test -x "$runtimeRoot/usr/bin/getent"
    "$runtimeRoot/usr/bin/getent" --version >/dev/null
  '';

in
assert !(disabled.systemd.services ? loom-project-preparation);
assert !(disabled.systemd.sockets ? loom-project-preparation);
assert builtins.all (a: a.assertion) enabled.assertions;
assert builtins.all (host: !(self.nixosConfigurations.${host}.config.systemd.services ? loom-project-preparation)) (builtins.attrNames self.nixosConfigurations);
assert policy.caller_uid == 1234 && policy.uid_base == fixtureUIDBase;
assert policy.reservations == enabled.loom.projectPreparation.otherReservations;
assert policy.runtime_manifest == builtins.unsafeDiscardStringContext "${runtime}/manifest.json";
assert policy.nspawn == builtins.unsafeDiscardStringContext "${pkgs.systemd}/bin/systemd-nspawn";
assert policy.getent == builtins.unsafeDiscardStringContext "${pkgs.getent}/bin/getent";
assert containsAll socket [ "Accept=false" "FlushPending=true" "FileDescriptorName=preparation" "SocketUser=1234" "SocketMode=0600" "Backlog=8" "ListenStream=/run/loom-project-preparation/prepare.sock" ];
assert containsAll helper [ "StandardInput=null" "Delegate=true" "DelegateSubgroup=supervisor" "MemoryMax=3G" "MemorySwapMax=0" "TasksMax=128" "CPUQuota=100%" "KillMode=control-group" "TimeoutStopSec=10s" "SendSIGKILL=true" "Restart=no" "OOMPolicy=kill" "PrivateMounts=true" "ProtectSystem=strict" "RuntimeMaxSec=315s" "User=root" ];
assert !(lib.hasInfix "@" helper);
assert !(lib.hasInfix "--" helper);
pkgs.runCommand "loom-project-preparation-rendered" {
  passthru = {
    rendered = { inherit socket helper hostConfig; runtimeManifest = "${runtime}/manifest.json"; };
    inherit runtime getentLinkCheck;
  };
} ''
  # This realized-output check fails the original tree whose getent link
  # targeted glibc.bin. Evaluating strings alone cannot establish this fact.
  ${getentLinkCheck} ${runtime.root} ${pkgs.getent}/bin/getent
  brokenRoot="$TMPDIR/original-broken-getent-root"
  mkdir -p "$brokenRoot/usr/bin"
  ln -s ${pkgs.glibc.bin}/bin/getent "$brokenRoot/usr/bin/getent"
  if ${getentLinkCheck} "$brokenRoot" ${pkgs.getent}/bin/getent; then
    echo "original broken getent output unexpectedly passed" >&2
    exit 1
  fi
  mkdir -p "$out"
  cp ${pkgs.writeText "preparation.socket" socket} "$out/loom-project-preparation.socket"
  cp ${pkgs.writeText "preparation.service" helper} "$out/loom-project-preparation.service"
  cp ${pkgs.writeText "preparation-config.json" hostConfig} "$out/project-preparation.json"
''
