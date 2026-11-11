{ config, lib, pkgs, ... }:
let
  cfg = config.loom.codexRuntime;
  executable = "/home/agents/.local/bin/codex";
  updaterPython = pkgs.python3.withPackages (p: [ p.websockets ]);
  updater = pkgs.writeShellScriptBin "loom-codex-update" ''
    exec ${updaterPython}/bin/python3 ${../files/codex/idle_update.py} "$@"
  '';
  messenger = pkgs.writeShellScriptBin "loom-codex-message" ''
    exec ${updaterPython}/bin/python3 ${../files/codex/delegation.py} "$@"
  '';
in {
  options.loom.codexRuntime = {
    enable = lib.mkEnableOption "boot-persistent native Codex under the shared agents account";
    recoveryEnable = lib.mkEnableOption "selected private Codex history capture for existing encrypted cloud protection";
    autoUpdateEnable = lib.mkEnableOption "daily stable Codex updates deferred until agents are idle";
  };

  config = lib.mkIf cfg.enable {
    assertions = [{
      assertion = config.loom.agents.enable;
      message = "Codex runtime requires the existing shared agents account.";
    }];
    users.users.agents.linger = true;
    users.users.agents.packages = [ messenger ] ++ lib.optional cfg.autoUpdateEnable updater;
    systemd.user.services.loom-codex-update = lib.mkIf cfg.autoUpdateEnable {
      description = "Idle-aware stable Codex update check";
      unitConfig.ConditionUser = "agents";
      environment = {
        HOME = "/home/agents";
        CODEX_HOME = "/home/agents/.codex";
        PATH = lib.mkForce "/home/agents/.local/bin:/etc/profiles/per-user/agents/bin:/run/current-system/sw/bin";
      };
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${updater}/bin/loom-codex-update";
        WorkingDirectory = "/home/agents";
        UMask = "0077";
        NoNewPrivileges = true;
        TimeoutStartSec = 600;
      };
    };
    systemd.user.timers.loom-codex-update = lib.mkIf cfg.autoUpdateEnable {
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnCalendar = "*-*-* 04:30:00 Europe/Amsterdam";
        Persistent = false;
        Unit = "loom-codex-update.service";
      };
    };
    # Activated only by a deferred update, never an always-running polling loop.
    systemd.user.timers.loom-codex-update-retry = lib.mkIf cfg.autoUpdateEnable {
      timerConfig = {
        OnActiveSec = "30min";
        Persistent = false;
        Unit = "loom-codex-update.service";
      };
    };
    systemd.tmpfiles.rules = lib.optionals cfg.recoveryEnable [
      "d /srv/loom/agents/mina/recovery-codex 2750 agents loom -"
    ];
    systemd.services.loom-codex-recovery = lib.mkIf cfg.recoveryEnable {
      description = "Selected private Codex durable-state capture";
      unitConfig.RequiresMountsFor = [ "/home/agents" "/srv/loom/agents/mina" ];
      serviceConfig = {
        Type = "oneshot";
        User = "agents";
        Group = "loom";
        ExecStart = "${pkgs.python3}/bin/python3 ${../files/codex-recovery/capture.py}";
        UMask = "0027";
        NoNewPrivileges = true;
        TimeoutStartSec = 300;
      };
    };
    systemd.timers.loom-codex-recovery = lib.mkIf cfg.recoveryEnable {
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnCalendar = "*-*-* 02:40:00 Europe/Amsterdam";
        Persistent = false;
        RandomizedDelaySec = 0;
      };
    };
    systemd.user.services.loom-codex-app-server = {
      description = "LOOM native Codex app-server (owner-coordinated updates)";
      wantedBy = [ "default.target" ];
      unitConfig = {
        ConditionUser = "agents";
        ConditionFileIsExecutable = executable;
      };
      # A package/config deployment must not interrupt an active conversation.
      restartIfChanged = false;
      environment = {
        HOME = "/home/agents";
        CODEX_HOME = "/home/agents/.codex";
        PATH = lib.mkForce "/home/agents/.local/bin:/etc/profiles/per-user/agents/bin:/run/current-system/sw/bin";
      };
      serviceConfig = {
        Type = "forking";
        # Native Codex has one PID backend, not a systemd backend. With its
        # updater disabled only the server forks; systemd supervises that PID.
        GuessMainPID = true;
        ExecStartPre = "${pkgs.python3}/bin/python3 ${../files/codex/daemon_settings.py}";
        ExecStart = "${executable} app-server daemon start";
        ExecStop = "${executable} app-server daemon stop";
        Restart = "on-failure";
        RestartSec = 5;
        TimeoutStartSec = 45;
        TimeoutStopSec = 330;
        KillMode = "mixed";
        WorkingDirectory = "/home/agents";
        UMask = "0077";
        NoNewPrivileges = true;
      };
    };
  };
}
