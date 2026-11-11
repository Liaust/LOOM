{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.orca;
  minaSelected = config.loom.mina.selected || config.loom.mina.enable || config.loom.mina.recoveryEnabled;
  identity = if minaSelected then "mina" else "morathustra";
  runtime = config.loom.${identity};
  accountHome = "/home/agents";
in {
  imports = [
    ./loom-agents.nix
    (lib.mkRenamedOptionModule [ "loom" "orca" "protonPassPackage" ] [ "loom" "agents" "protonPassPackage" ])
  ];
  options.loom.orca = {
    enable = lib.mkEnableOption "the private headless ORCA runtime";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.orca-headless.override {
        agentIdentity = identity;
        macComputerUseEnabled = runtime.macComputerUseEnabled;
      };
      description = "Pinned ORCA AppImage package used by the headless service.";
    };

    pairingAddress = lib.mkOption {
      type = lib.types.str;
      default = "10.44.0.2";
      description = "Private address advertised to ORCA clients.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 6768;
      description = "Private ORCA WebSocket listener port.";
    };

    allowedInterface = lib.mkOption {
      type = lib.types.str;
      default = "wg0";
      description = "WireGuard interface allowed to reach the ORCA listener.";
    };
  };

  config = lib.mkIf cfg.enable {
    loom.agents.enable = lib.mkDefault true;
    assertions = [{
      assertion = config.loom.agents.enable;
      message = "The optional retained ORCA runtime requires the independent shared agents environment.";
    }];
    environment.systemPackages = [ cfg.package pkgs.xorg.xorgserver ];
    networking.firewall.interfaces.${cfg.allowedInterface}.allowedTCPPorts = [ cfg.port ];

    systemd.services."orca-serve" = {
      description = "ORCA runtime server";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [
        "network-online.target"
        "wireguard-wg0.service"
      ];

      unitConfig = {
        StartLimitIntervalSec = 300;
        StartLimitBurst = 5;
      };

      path = [
        # Follow current tools without restarting terminal sessions whenever
        # an unrelated system package changes the generation's store path.
        "/run/current-system/sw"
        pkgs.bashInteractive
        pkgs.git
        pkgs.nodejs_22
        pkgs.ripgrep
        pkgs.xorg.xorgserver
      ];

      environment = {
        HOME = accountHome;
        XDG_CONFIG_HOME = "${accountHome}/.config";
        LIBGL_ALWAYS_SOFTWARE = "1";
        # ORCA treats APPIMAGE as the stable outer executable when installing
        # its own headless CLI dispatchers. Point it at the FHS wrapper so the
        # generated commands remain executable on NixOS after every restart.
        APPIMAGE = "${cfg.package}/bin/orca";
      };

      serviceConfig = {
        Type = "simple";
        User = "agents";
        Group = "agents";
        WorkingDirectory = accountHome;
        ExecStart = "${cfg.package}/bin/orca ${lib.escapeShellArgs [
          "serve"
          "--port"
          (toString cfg.port)
          "--pairing-address"
          cfg.pairingAddress
          "--json"
        ]}";
        StandardOutput = "journal";
        StandardError = "journal";
        # The Nix AppImage FHS wrapper is the main PID. Signal the whole cgroup
        # so the wrapped Electron runtime and ORCA-owned Xvfb stop with it.
        KillMode = "control-group";
        Restart = "on-failure";
        RestartPreventExitStatus = 3;
        RestartSec = 5;
        NoNewPrivileges = true;
        UMask = "0027";
      };
    };
  };
}
