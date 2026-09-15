# Keep legacy compatibility matrices independent of Main's selected persona.
# Reuse the host, excluding the later operator-only desktop opt-in. Each Mac
# matrix explicitly enables its selected connector; unrelated matrices stay off.
system:
let
  definitions = system.options.loom.mina.enable.definitionsWithLocations
    ++ system.options.loom.morathustra.enable.definitionsWithLocations;
  hosts = builtins.filter (d: builtins.match ".*/nix/hosts/hardware-main/configuration.nix" d.file != null) definitions;
  hostPath = (builtins.head hosts).file;
in system.extendModules {
  modules = [
    { disabledModules = [ hostPath ]; }
    ({ config, lib, ... }:
      let host = import hostPath { inherit config lib; };
      in if host.loom ? mina then host // {
        loom = builtins.removeAttrs host.loom [ "mina" ] // {
          morathustra = builtins.removeAttrs host.loom.mina [ "retainedMorathustra" "selected" "macComputerUseEnabled" "macComputerUseBindingSHA256" ];
        };
      } else host)
  ];
}
