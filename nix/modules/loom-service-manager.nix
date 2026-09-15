{ config, lib, pkgs, self, ... }:

let
  cfg = config.loom.serviceManager;
  system = pkgs.stdenv.hostPlatform.system;
in
{
  options.loom.serviceManager = {
    enable = lib.mkEnableOption "the allowlisted LOOM systemd service-manager helper";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.loom-service-manager;
      description = "Package providing the narrow loom-service-manager helper.";
    };
    allowlistPath = lib.mkOption {
      type = lib.types.str;
      default = "/etc/loom/service-allowlist.yaml";
      description = "Reviewed node-local service allowlist consumed by the helper.";
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ];
    assertions = [{
      assertion = cfg.allowlistPath != "";
      message = "loom.serviceManager.allowlistPath must name a reviewed allowlist file.";
    }];
  };
}
