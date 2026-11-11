{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.mina;
  selected = cfg.selected || cfg.enable || cfg.recoveryEnabled;
  accountTools = pkgs.callPackage ../packages/morathustra-external-tools.nix {
    agentIdentity = "mina";
    basecampAccountID = cfg.externalAccountBinding.basecampAccountID;
    basecamp-cli = self.packages.${pkgs.stdenv.hostPlatform.system}.basecamp-cli;
  };
  browser = self.packages.${pkgs.stdenv.hostPlatform.system}.agent-browser;
  boxTools = pkgs.callPackage ../packages/box-account-tools.nix {
    basecamp-cli = self.packages.${pkgs.stdenv.hostPlatform.system}.basecamp-cli;
    loom = self.packages.${pkgs.stdenv.hostPlatform.system}.loom;
  };
  macBridge = self.packages.${pkgs.stdenv.hostPlatform.system}.mac-computer-use-bridge.overrideAttrs (_: {
    pinnedBindingSHA256 = if cfg.macComputerUseBindingSHA256 == null then "" else cfg.macComputerUseBindingSHA256;
  });
in {
  # Tool selection is deliberately independent of the retired/active harness.
  config = lib.mkIf selected {
    users.users.agents.packages = [
      self.packages.${pkgs.stdenv.hostPlatform.system}.basecamp-cli pkgs.gh
    ] ++ lib.optional (config.loom.boxes != { }) (lib.hiPrio boxTools)
      ++ lib.optionals (cfg.externalAccountsEnabled && cfg.externalAccountBinding != null) accountTools
      ++ lib.optionals cfg.browserEnabled [ browser browser.chromium ]
      ++ lib.optional cfg.macComputerUseEnabled macBridge;

    environment.etc = {
      "loom-codex/mina.config.toml".source = ../files/codex/mina.config.toml;
    } // lib.optionalAttrs (cfg.externalAccountsEnabled && cfg.externalAccountBinding != null) {
      "loom-mina-accounts.json" = {
        mode = "symlink";
        text = builtins.toJSON {
          schema_version = 1;
          github = { host = "github.com"; login = cfg.externalAccountBinding.githubLogin; id = cfg.externalAccountBinding.githubID; };
          basecamp = {
            profile = "mina";
            identity_id = cfg.externalAccountBinding.basecampIdentityID;
            account_id = cfg.externalAccountBinding.basecampAccountID;
            person_id = cfg.externalAccountBinding.basecampPersonID;
            email = cfg.externalAccountBinding.basecampEmail;
          };
        };
      };
    };
  };
}
