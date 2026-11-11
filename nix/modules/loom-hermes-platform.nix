{ config, lib, pkgs, self, ... }:
let
  cfg = config.loom.hermesPlatform;
  workspace = "/srv/loom/agents/mina";
  home = "${workspace}/.hermes";
  state = "/var/lib/loom/mina-hermes";
  codexDispatch = pkgs.writeShellScriptBin "loom-hermes-codex-dispatch" ''
    exec ${native.hermesVenv}/bin/python3 -B ${../files/hermes-platform/codex_dispatch.py} "$@"
  '';
  native = self.packages.${pkgs.stdenv.hostPlatform.system}.hermes-native-agent;
  boxAgentCommands = pkgs.writeText "loom-box-agent-commands.json" (builtins.toJSON {
    basecamp = "${self.packages.${pkgs.stdenv.hostPlatform.system}.basecamp-cli}/bin/basecamp";
    gh = "${pkgs.gh}/bin/gh";
  });
  boxAgent = pkgs.writeShellScriptBin "loom-box-agent-prepare" ''
    exec ${native.hermesVenv}/bin/python3 -B ${../files/hermes-platform/box_agent.py} \
      --native-home ${home} --boxes-file /etc/loom-boxes.json \
      --templates ${../../ai-loom-pack/templates/box-agent} \
      --policy-source ${../../ai-loom-pack/host-instructions/boxes/company/WORKFLOW.md} \
      --python ${native.hermesVenv}/bin/python3 \
      --tool-launcher ${../files/hermes-platform/company_tools.py} \
      --tool-path /run/current-system/sw/bin:/etc/profiles/per-user/agents/bin:/home/agents/.local/bin \
      --commands-file ${boxAgentCommands} --external-contexts ${state}/external-contexts.json "$@"
  '';
  runtime = pkgs.writeShellScriptBin "loom-hermes-runtime" ''
    exec ${pkgs.python3}/bin/python3 -B ${../files/hermes-platform/runtime.py} "$@"
  '';
  recovery = pkgs.writeShellScriptBin "loom-hermes-native-recovery" ''
    exec ${native.hermesVenv}/bin/python3 -B ${../files/hermes-platform/capture.py} \
      --binary ${native}/bin/hermes --public-key ${lib.escapeShellArg config.loom.mina.recoveryPublicKey} \
      --external-contexts ${state}/external-contexts.json "$@"
  '';
  scheduleBinding = pkgs.writeText "loom-native-hermes-schedules.json" (builtins.toJSON {
    source = { host = config.loom.nodeId; profile = "mina"; revision = native.upstreamRevision; };
    inherit home;
    caller = config.loom.user;
    boxes_file = if config.loom.boxes == { } then "" else "/etc/loom-boxes.json";
  });
  observer = self.packages.${pkgs.stdenv.hostPlatform.system}.loom.overrideAttrs (old: {
    pname = "loom-native-hermes-observer";
    subPackages = [ "cmd/loom-hermes-schedules" ];
    ldflags = (old.ldflags or []) ++ [ "-X main.bindingPath=${scheduleBinding}" ];
    postInstall = "";
  });
  projectSchedules = pkgs.writeScriptBin "loom-native-hermes-project-schedules" ''
    #!${native.hermesVenv}/bin/python3
    import json, os, sys
    os.environ.clear()
    os.environ["HOME"] = "/home/agents"
    os.environ["HERMES_HOME"] = "${home}"
    os.environ["PYTHONDONTWRITEBYTECODE"] = "1"
    sys.path.insert(0, "${../../internal/hermesschedules}")
    from native_project import serve
    with open("${scheduleBinding}") as stream:
        serve(json.load(stream))
  '';
  bootstrap = pkgs.writeShellScript "loom-hermes-bootstrap" ''
    exec ${native.hermesVenv}/bin/python3 -B ${../files/hermes-platform/bootstrap.py} \
      --workspace ${workspace} --state ${state}
  '';
in {
  options.loom.hermesPlatform = {
    enable = lib.mkEnableOption "the private Main-owned native Hermes conversational backend";
    listenAddress = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1";
      description = "Loopback or explicitly selected private WireGuard address, never a wildcard.";
    };
    port = lib.mkOption { type = lib.types.port; default = 9119; };
    interface = lib.mkOption { type = lib.types.str; default = "wg0"; };
    timeZone = lib.mkOption {
      type = lib.types.str;
      default = if config.time.timeZone == null then "UTC" else config.time.timeZone;
      description = "Timezone for native scheduling and the recovery timer.";
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ codexDispatch ];
    assertions = [
      { assertion = config.loom.mina.selected && !config.loom.mina.enable;
        message = "Native Hermes platform requires MINA selection and the legacy gateway disabled."; }
      { assertion = cfg.listenAddress == "127.0.0.1" || lib.hasPrefix "10.44.0." cfg.listenAddress;
        message = "Hermes control plane must bind loopback or the existing private WireGuard network."; }
    ];
    users.users.agents.packages = [ native runtime recovery boxAgent ];
    systemd.tmpfiles.rules = [
      "d ${state} 0700 agents agents - -"
      "d ${workspace}/recovery-hermes-native 2750 agents loom - -"
      "d /run/loom-mina-schedules 0755 root root - -"
    ];
    systemd.services.loomd.environment = {
      LOOM_HERMES_SCHEDULES_PROFILE = "mina";
      LOOM_HERMES_SCHEDULES_HOME = home;
      LOOM_HERMES_SCHEDULES_REVISION = native.upstreamRevision;
      LOOM_HERMES_SCHEDULES_SOCKET = "/run/loom-mina-schedules/observe.sock";
      LOOM_HERMES_PROJECT_SCHEDULES = "1";
    };
    systemd.sockets.loom-mina-schedules = {
      wantedBy = [ "sockets.target" ];
      socketConfig = {
        ListenStream = "/run/loom-mina-schedules/observe.sock";
        Accept = true;
        SocketUser = config.loom.user;
        SocketMode = "0600";
        MaxConnections = 4;
        RemoveOnStop = true;
      };
    };
    systemd.services."loom-mina-schedules@" = {
      description = "Bounded native Hermes schedule inventory";
      serviceConfig = {
        User = "agents";
        Group = "agents";
        ExecStart = "${observer}/bin/loom-hermes-schedules";
        StandardInput = "socket";
        StandardOutput = "socket";
        StandardError = "null";
        RuntimeMaxSec = 6;
        NoNewPrivileges = true;
        UMask = "0077";
      };
    };
    systemd.sockets.loom-mina-project-schedules = {
      wantedBy = [ "sockets.target" ];
      socketConfig = {
        ListenStream = "/run/loom-mina-schedules/project.sock";
        Accept = true;
        SocketUser = config.loom.user;
        SocketMode = "0600";
        MaxConnections = 4;
        RemoveOnStop = true;
      };
    };
    systemd.services."loom-mina-project-schedules@" = {
      description = "Project declaration to native Hermes schedule adapter";
      serviceConfig = {
        User = "agents";
        Group = "agents";
        ExecStart = "${projectSchedules}/bin/loom-native-hermes-project-schedules";
        StandardInput = "socket";
        StandardOutput = "socket";
        StandardError = "null";
        RuntimeMaxSec = 15;
        NoNewPrivileges = true;
        UMask = "0077";
      };
    };
    networking.firewall.interfaces.${cfg.interface}.allowedTCPPorts =
      lib.optional (cfg.listenAddress != "127.0.0.1") cfg.port;
    systemd.services.loom-hermes-platform = {
      description = "MINA native Hermes private conversational backend";
      wantedBy = [ "multi-user.target" ];
      # Binding the selected private address needs its interface, not global
      # Wi-Fi/DHCP startup completion. Remote requests handle network failures.
      requires = lib.optional (cfg.listenAddress != "127.0.0.1") "wireguard-${cfg.interface}.service";
      after = lib.optional (cfg.listenAddress != "127.0.0.1") "wireguard-${cfg.interface}.service";
      unitConfig.RequiresMountsFor = [ workspace ];
      # Nix may stage new binaries during a conversation. Activation has its
      # own native admission fence; connected idle clients are not active work.
      restartIfChanged = false;
      environment = {
        HOME = "/home/agents";
        HERMES_HOME = home;
        PYTHONDONTWRITEBYTECODE = "1";
        PATH = lib.mkForce (lib.makeBinPath [ native pkgs.bash pkgs.coreutils pkgs.git pkgs.openssh pkgs.ripgrep pkgs.curl ]
          + ":/etc/profiles/per-user/agents/bin:/home/agents/.local/bin:/run/current-system/sw/bin");
      } // lib.optionalAttrs config.loom.agentDesktop.enable {
        HERMES_CUA_DRIVER_CMD = "/run/current-system/sw/bin/loom-hermes-desktop";
        XDG_RUNTIME_DIR = "/run/user/${toString config.users.users.agents.uid}";
      };
      serviceConfig = {
        Type = "simple";
        User = "agents";
        Group = "agents";
        WorkingDirectory = workspace;
        UMask = "0077";
        EnvironmentFile = "-${state}/dashboard.env";
        ExecStartPre = bootstrap;
        ExecStart = "${native}/bin/hermes serve --host ${cfg.listenAddress} --port ${toString cfg.port}";
        KillMode = "mixed";
        TimeoutStopSec = 120;
        Restart = "on-failure";
        RestartSec = 5;
      };
    };
    systemd.services.loom-hermes-scheduler = {
      description = "Native Hermes profile scheduler (no messaging channels)";
      wantedBy = [ "multi-user.target" ];
      requires = [ "loom-hermes-platform.service" ];
      after = [ "loom-hermes-platform.service" ];
      restartIfChanged = false;
      unitConfig.RequiresMountsFor = [ workspace ];
      environment = config.systemd.services.loom-hermes-platform.environment // {
        PATH = lib.mkForce config.systemd.services.loom-hermes-platform.environment.PATH;
        XDG_RUNTIME_DIR = "/run/user/${toString config.users.users.agents.uid}";
        TZ = cfg.timeZone;
      };
      serviceConfig = {
        Type = "simple";
        User = "agents";
        Group = "agents";
        WorkingDirectory = workspace;
        UMask = "0077";
        ExecStart = "${native}/bin/hermes gateway run";
        KillMode = "mixed";
        TimeoutStopSec = 330;
        Restart = "on-failure";
        RestartSec = 10;
      };
    };
    systemd.services.loom-hermes-native-recovery = {
      description = "Signed current native Hermes durable-state capture";
      after = [ "loom-hermes-platform.service" ];
      unitConfig.RequiresMountsFor = [ workspace state ];
      serviceConfig = {
        Type = "oneshot";
        User = "agents";
        Group = "loom";
        WorkingDirectory = workspace;
        UMask = "0027";
        ExecStart = "${recovery}/bin/loom-hermes-native-recovery";
        TimeoutStartSec = 300;
        NoNewPrivileges = true;
      };
    };
    systemd.timers.loom-hermes-native-recovery = {
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnCalendar = "*-*-* 02:35:00 ${cfg.timeZone}";
        Persistent = false;
        RandomizedDelaySec = 0;
      };
    };
    systemd.services.loom-hermes-platform-activate = {
      description = "Activate reviewed Hermes package after native work drains";
      wantedBy = [ "multi-user.target" ];
      wants = [ "loom-hermes-platform.service" "loom-hermes-scheduler.service" ];
      after = [ "loom-hermes-platform.service" "loom-hermes-scheduler.service" ];
      restartTriggers = [ native ];
      path = [ pkgs.systemd ];
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${runtime}/bin/loom-hermes-runtime activate --entry ${native.hermesVenv}/bin/hermes";
        UMask = "0077";
        TimeoutStartSec = 420;
      };
    };
    systemd.timers.loom-hermes-platform-activate-retry = {
      timerConfig = {
        OnActiveSec = "30min";
        Persistent = false;
        Unit = "loom-hermes-platform-activate.service";
      };
    };
  };
}
