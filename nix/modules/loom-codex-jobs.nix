{ config, lib, pkgs, ... }:
let
  cfg = config.loom.codexJobs;
  source = ../files/codex-jobs/bridge.py;
  python = pkgs.python3.withPackages (p: [ p.pyyaml ]);
  boxesEnabled = config.loom.boxes != { };
  boxArguments = lib.optionalString boxesEnabled " --boxes-file /etc/loom-boxes.json --box-contexts-file /etc/loom-codex-box-contexts.json";
  client = pkgs.writeShellScriptBin "loom-codex-exec" ''
    exec ${python}/bin/python3 ${source} exec "$@"
  '';
  reviewGate = pkgs.writeShellScriptBin "loom-provenance-review-gate" ''
    exec ${pkgs.python3}/bin/python3 ${../../scripts/loom-provenance-review-gate} "$@"
  '';
in {
  options.loom.codexJobs.enable = lib.mkEnableOption "MINA/project/custom Codex jobs using the existing agents login";
  options.loom.codexJobs.boxContexts = lib.mkOption {
    type = lib.types.attrsOf (lib.types.attrsOf lib.types.str);
    default = { };
    description = "Explicit Box-keyed company CLI environment paths, never secret values. Missing bindings keep that Box unavailable for Codex jobs.";
  };
  config = lib.mkIf cfg.enable {
    environment.etc."loom-codex-box-contexts.json" = lib.mkIf boxesEnabled {
      text = builtins.toJSON cfg.boxContexts;
      mode = "0444";
    };
    assertions = [{
      assertion = config.loom.agents.enable;
      message = "Codex jobs require the existing agents account and its operator-managed Codex login.";
    }];
    environment.systemPackages = [ client reviewGate pkgs.python3 ];
    systemd.tmpfiles.rules = [
      "d /run/loom-codex-jobs 0755 root root -"
      "d /home/agents/tasks 0700 agents agents -"
    ];
    systemd.sockets.loom-codex-jobs = {
      wantedBy = [ "sockets.target" ];
      socketConfig = {
        ListenStream = "/run/loom-codex-jobs/exec.sock";
        Accept = true;
        SocketUser = config.loom.user;
        SocketMode = "0600";
        MaxConnections = 4;
        Backlog = 4;
        RemoveOnStop = true;
      };
    };
    systemd.services."loom-codex-jobs@" = {
      description = "One LOOM Codex call under the agents identity";
      unitConfig.RequiresMountsFor = [ config.loom.boxPath "/home/agents" ];
      path = [ pkgs.git pkgs.coreutils pkgs.bash ];
      environment = {
        HOME = "/home/agents";
        CODEX_HOME = "/home/agents/.codex";
        PATH = lib.mkForce "/home/agents/.local/bin:/etc/profiles/per-user/agents/bin:/run/current-system/sw/bin";
      };
      serviceConfig = {
        Type = "exec";
        User = "agents";
        Group = "agents";
        ExecStart = "${python}/bin/python3 ${source} serve --projects-root ${config.loom.boxPath}/Projects --caller ${config.loom.user}${boxArguments}";
        StandardInput = "socket";
        StandardOutput = "socket";
        StandardError = "journal";
        # The bridge enforces each request's timeout (maximum one hour).
        RuntimeMaxSec = 3610;
        TimeoutStopSec = 2;
        KillMode = "control-group";
        NoNewPrivileges = true;
        UMask = "0077";
        PrivateTmp = true;
        ProtectSystem = "full";
      };
    };
  };
}
