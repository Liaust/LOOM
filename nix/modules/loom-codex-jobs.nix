{ config, lib, pkgs, ... }:
let
  cfg = config.loom.codexJobs;
  source = ../files/codex-jobs/bridge.py;
  client = pkgs.writeShellScriptBin "loom-codex-exec" ''
    exec ${pkgs.python3}/bin/python3 ${source} exec "$@"
  '';
in {
  options.loom.codexJobs.enable = lib.mkEnableOption "project Codex jobs using the existing agents login";
  config = lib.mkIf cfg.enable {
    assertions = [{
      assertion = config.loom.orca.enable;
      message = "Codex jobs require the existing agents account and its operator-managed Codex login.";
    }];
    environment.systemPackages = [ client ];
    systemd.tmpfiles.rules = [ "d /run/loom-codex-jobs 0755 root root -" ];
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
      description = "One LOOM project Codex call under the agents identity";
      unitConfig.RequiresMountsFor = [ config.loom.boxPath "/home/agents" ];
      path = [ pkgs.git pkgs.coreutils pkgs.bash ];
      environment = {
        HOME = "/home/agents";
        CODEX_HOME = "/home/agents/.codex";
      };
      serviceConfig = {
        Type = "exec";
        User = "agents";
        Group = "agents";
        ExecStart = "${pkgs.python3}/bin/python3 ${source} serve --projects-root ${config.loom.boxPath}/Projects --caller ${config.loom.user}";
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
