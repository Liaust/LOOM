{ config, lib, pkgs, ... }:
let
  cfg = config.loom.tylerMail;
  profile = "/srv/loom/agents/mina/.hermes/profiles/tyler";
  home = "${profile}/home";
  private = "${home}/.config/loom-proton";
  cache = "/var/cache/loom-agent-tools/tyler/proton-bridge";
  bridge = pkgs.callPackage ../packages/protonmail-bridge-current.nix { };
  python = pkgs.python3.withPackages (p: [ p.cryptography p.tomlkit ]);
  path = lib.makeBinPath [ bridge pkgs.bash pkgs.pass pkgs.gnupg pkgs.coreutils pkgs.himalaya pkgs.msmtp ];
  environment = {
    HOME = home;
    XDG_CONFIG_HOME = "${home}/.config";
    XDG_CACHE_HOME = "${cache}/cache";
    XDG_DATA_HOME = "${cache}/data";
    GNUPGHOME = "${private}/gnupg";
    PASSWORD_STORE_DIR = "${private}/pass";
    PATH = path;
    LANG = "C.UTF-8";
    DBUS_SESSION_BUS_ADDRESS = "unix:path=/dev/null";
  };
  binding = pkgs.writeText "loom-tyler-mail.json" (builtins.toJSON {
    inherit environment home private cache;
    bridge = "${bridge}/bin/protonmail-bridge";
    himalaya = "${pkgs.himalaya}/bin/himalaya";
    msmtp = "${pkgs.msmtp}/bin/msmtp";
    gpg = "${pkgs.gnupg}/bin/gpg";
    pass = "${pkgs.pass}/bin/pass";
    systemctl = "${pkgs.systemd}/bin/systemctl";
    unit = "loom-tyler-proton-bridge.service";
    priority_address = cfg.senderAddress;
    display_name = cfg.displayName;
    profile_marker = "${profile}/loom-box-owner.json";
  });
  cli = pkgs.writeShellScriptBin "loom-tyler-mail" ''
    exec ${python}/bin/python3 -B ${../files/hermes-platform}/tyler_mail.py --binding ${binding} "$@"
  '';
in {
  options.loom.tylerMail = {
    enable = lib.mkEnableOption "Tyler's Main-owned Proton Mail Bridge and explicit mail client";
    senderAddress = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Operator-verified sender address; no account is selected by the source template.";
    };
    displayName = lib.mkOption {
      type = lib.types.str;
      default = "Tyler";
      description = "Display name for the explicitly configured mail sender.";
    };
  };
  config = lib.mkIf cfg.enable {
    assertions = [{
      assertion = config.loom.hermesPlatform.enable && config.loom.agents.enable;
      message = "Tyler mail requires the existing native Hermes platform and agents account.";
    } {
      assertion = builtins.match "[^@[:space:]]+@[^@[:space:]]+[.][^@[:space:]]+" cfg.senderAddress != null
        && cfg.displayName != "";
      message = "Tyler mail requires an explicit senderAddress and displayName verified by the operator.";
    }];
    users.users.agents.linger = true;
    users.users.agents.packages = [ cli ];
    systemd.tmpfiles.rules = map (directory: "d ${directory} 0700 agents agents - -") [
      "/var/cache/loom-agent-tools" "/var/cache/loom-agent-tools/tyler"
      cache "${cache}/cache" "${cache}/data"
    ];
    systemd.user.services.loom-tyler-proton-bridge = {
      description = "Tyler Proton Mail Bridge (loopback IMAP/SMTP)";
      wantedBy = [ "default.target" ];
      unitConfig = {
        ConditionUser = "agents";
        ConditionPathExists = "${private}/pass/.gpg-id";
        StartLimitIntervalSec = 600;
        StartLimitBurst = 3;
      };
      restartIfChanged = false;
      environment = environment // { PATH = lib.mkForce path; };
      serviceConfig = {
        Type = "simple";
        ExecStart = "${cli}/bin/loom-tyler-mail serve";
        WorkingDirectory = home;
        UMask = "0077";
        Restart = "on-failure";
        RestartSec = 30;
        NoNewPrivileges = true;
        PrivateTmp = true;
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        Nice = 10;
      };
    };
  };
}
